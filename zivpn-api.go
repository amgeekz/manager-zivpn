// /etc/zivpn/api/zivpn-api.go
package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"math/rand"
)

const (
	ConfigFile   = "/etc/zivpn/config.json"
	UserDB       = "/etc/zivpn/users.db"
	DomainFile   = "/etc/zivpn/domain"
	ApiKeyFile   = "/etc/zivpn/apikey"
	BackupDir    = "/etc/zivpn/backups"
	RcloneRemote = "drive:ZIVPN-BACKUP" // remote:path
	Port         = ":8080"
)

var (
	AuthToken   = ""
	mutex       = &sync.Mutex{}
	backupMutex = &sync.Mutex{}
)

// Structs
type Config struct {
	Listen string `json:"listen"`
	Cert   string `json:"cert"`
	Key    string `json:"key"`
	Obfs   string `json:"obfs"`
	Auth   struct {
		Mode   string   `json:"mode"`
		Config []string `json:"config"`
	} `json:"auth"`
}

type UserRequest struct {
	Password string `json:"password"`
	Days     int    `json:"days"`
}

type BackupRequest struct {
	BackupID string `json:"backup_id"`
}

type BackupInfo struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Date     string `json:"date"`
	Domain   string `json:"domain"`
	Size     int64  `json:"size"`
}

type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// main
func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// load api key if available
	if keyBytes, err := ioutil.ReadFile(ApiKeyFile); err == nil {
		AuthToken = strings.TrimSpace(string(keyBytes))
	} else {
		log.Printf("Warning: apikey file not found: %v", err)
	}

	if err := os.MkdirAll(BackupDir, 0755); err != nil {
		log.Fatalf("Unable to create backup dir: %v", err)
	}

	http.HandleFunc("/api/user/create", authMiddleware(createUser))
	http.HandleFunc("/api/user/delete", authMiddleware(deleteUser))
	http.HandleFunc("/api/user/renew", authMiddleware(renewUser))
	http.HandleFunc("/api/users", authMiddleware(listUsers))
	http.HandleFunc("/api/info", authMiddleware(getSystemInfo))
	http.HandleFunc("/api/backup", authMiddleware(handleBackup))
	http.HandleFunc("/api/backup/list", authMiddleware(listBackups))
	http.HandleFunc("/api/restore", authMiddleware(handleRestore))
	http.HandleFunc("/api/backup/cleanup", authMiddleware(cleanupOldBackups))

	log.Printf("ZiVPN API listening on %s", Port)
	log.Fatal(http.ListenAndServe(Port, nil))
}

// middleware & helpers
func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if AuthToken != "" {
			if r.Header.Get("X-API-Key") != AuthToken {
				jsonResponse(w, http.StatusUnauthorized, false, "Unauthorized", nil)
				return
			}
		} // if AuthToken empty => allow (useful for local testing)
		next(w, r)
	}
}

func jsonResponse(w http.ResponseWriter, status int, success bool, message string, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Response{Success: success, Message: message, Data: data})
}

func generateBackupID() string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 12)
	for i := range b {
		b[i] = charset[r.Intn(len(charset))]
	}
	return string(b)
}

// users
func createUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
		return
	}
	var req UserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, false, "Invalid request body", nil)
		return
	}
	if req.Password == "" || req.Days <= 0 {
		jsonResponse(w, http.StatusBadRequest, false, "Password dan days harus valid", nil)
		return
	}

	mutex.Lock()
	defer mutex.Unlock()

	cfg, err := loadConfig()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Failed reading config", nil)
		return
	}
	for _, u := range cfg.Auth.Config {
		if u == req.Password {
			jsonResponse(w, http.StatusConflict, false, "User already exists", nil)
			return
		}
	}

	cfg.Auth.Config = append(cfg.Auth.Config, req.Password)
	if err := saveConfig(cfg); err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Failed saving config", nil)
		return
	}

	exp := time.Now().Add(time.Duration(req.Days) * 24 * time.Hour).Format("2006-01-02")
	entry := fmt.Sprintf("%s | %s\n", req.Password, exp)
	if err := appendToFile(UserDB, entry); err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Failed write userdb", nil)
		return
	}

	_ = restartAll()

	jsonResponse(w, http.StatusOK, true, "User created", map[string]string{
		"password": req.Password,
		"expired":  exp,
	})
}

func deleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
		return
	}
	var req UserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, false, "Invalid request body", nil)
		return
	}

	mutex.Lock()
	defer mutex.Unlock()

	cfg, err := loadConfig()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Failed reading config", nil)
		return
	}

	var newAuth []string
	found := false
	for _, u := range cfg.Auth.Config {
		if u == req.Password {
			found = true
		} else {
			newAuth = append(newAuth, u)
		}
	}
	if !found {
		jsonResponse(w, http.StatusNotFound, false, "User not found", nil)
		return
	}
	cfg.Auth.Config = newAuth
	if err := saveConfig(cfg); err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Failed saving config", nil)
		return
	}

	users, _ := loadUsers()
	var newUsers []string
	for _, line := range users {
		parts := strings.Split(line, "|")
		if len(parts) > 0 && strings.TrimSpace(parts[0]) == req.Password {
			continue
		}
		newUsers = append(newUsers, line)
	}
	if err := saveUsers(newUsers); err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Failed saving users", nil)
		return
	}

	_ = restartAll()
	jsonResponse(w, http.StatusOK, true, "User deleted", nil)
}

func renewUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
		return
	}
	var req UserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, false, "Invalid request body", nil)
		return
	}
	if req.Password == "" || req.Days <= 0 {
		jsonResponse(w, http.StatusBadRequest, false, "Invalid params", nil)
		return
	}

	mutex.Lock()
	defer mutex.Unlock()

	users, _ := loadUsers()
	found := false
	var newUsers []string
	for _, line := range users {
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		pass := strings.TrimSpace(parts[0])
		expStr := strings.TrimSpace(parts[1])
		if pass == req.Password {
			found = true
			old, err := time.Parse("2006-01-02", expStr)
			if err != nil || old.Before(time.Now()) {
				old = time.Now()
			}
			newExp := old.Add(time.Duration(req.Days) * 24 * time.Hour).Format("2006-01-02")
			newUsers = append(newUsers, fmt.Sprintf("%s | %s", pass, newExp))
		} else {
			newUsers = append(newUsers, line)
		}
	}
	if !found {
		jsonResponse(w, http.StatusNotFound, false, "User not found", nil)
		return
	}
	if err := saveUsers(newUsers); err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Failed saving users", nil)
		return
	}

	_ = restartAll()
	jsonResponse(w, http.StatusOK, true, "User renewed", nil)
}

func listUsers(w http.ResponseWriter, r *http.Request) {
	users, _ := loadUsers()
	type U struct {
		Password string `json:"password"`
		Expired  string `json:"expired"`
		Status   string `json:"status"`
	}
	var out []U
	today := time.Now().Format("2006-01-02")
	for _, line := range users {
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		p := strings.TrimSpace(parts[0])
		e := strings.TrimSpace(parts[1])
		status := "Active"
		if e < today {
			status = "Expired"
		}
		out = append(out, U{Password: p, Expired: e, Status: status})
	}
	jsonResponse(w, http.StatusOK, true, "OK", out)
}

func getSystemInfo(w http.ResponseWriter, r *http.Request) {
	ipPub, _ := exec.Command("curl", "-s", "ifconfig.me").Output()
	ipPriv, _ := exec.Command("hostname", "-I").Output()
	domain := "Unknown"
	if b, err := ioutil.ReadFile(DomainFile); err == nil {
		domain = strings.TrimSpace(string(b))
	}
	info := map[string]interface{}{
		"public_ip":  strings.TrimSpace(string(ipPub)),
		"private_ip": func() string { f := strings.Fields(string(ipPriv)); if len(f) > 0 { return f[0] }; return "" }(),
		"domain":     domain,
	}
	jsonResponse(w, http.StatusOK, true, "OK", info)
}

// backup & restore
func handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
		return
	}

	backupMutex.Lock()
	defer backupMutex.Unlock()

	domain := "unknown"
	if b, err := ioutil.ReadFile(DomainFile); err == nil {
		domain = strings.TrimSpace(string(b))
	}

	id := generateBackupID()
	filename := fmt.Sprintf("%s.zip", id)
	tempFile := filepath.Join(BackupDir, filename)

	files := []string{
		ConfigFile,
		UserDB,
		DomainFile,
		ApiKeyFile,
		"/etc/zivpn/bot-config.json",
		"/etc/zivpn/zivpn.crt",
		"/etc/zivpn/zivpn.key",
	}

	if err := createZip(tempFile, files); err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Failed creating zip: "+err.Error(), nil)
		return
	}

	// upload
	if out, err := exec.Command("rclone", "copy", tempFile, RcloneRemote).CombinedOutput(); err != nil {
		_ = os.Remove(tempFile)
		jsonResponse(w, http.StatusInternalServerError, false, "Rclone upload failed: "+string(out), nil)
		return
	}

	// remove local
	_ = os.Remove(tempFile)

	jsonResponse(w, http.StatusOK, true, "Backup success", map[string]string{
		"backup_id": id,
		"filename":  filename,
	})
}

func handleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
		return
	}
	var req BackupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, false, "Invalid request body", nil)
		return
	}
	if req.BackupID == "" {
		jsonResponse(w, http.StatusBadRequest, false, "Missing backup ID", nil)
		return
	}

	temp := filepath.Join("/tmp", req.BackupID+".zip")
	// copy from rclone remote to /tmp
	if out, err := exec.Command("rclone", "copy", RcloneRemote+"/"+req.BackupID+".zip", "/tmp/").CombinedOutput(); err != nil {
		_ = os.Remove(temp)
		jsonResponse(w, http.StatusInternalServerError, false, "Rclone copy failed: "+string(out), nil)
		return
	}

	zr, err := zip.OpenReader(temp)
	if err != nil {
		_ = os.Remove(temp)
		jsonResponse(w, http.StatusInternalServerError, false, "Failed open zip: "+err.Error(), nil)
		return
	}
	defer zr.Close()

	for _, f := range zr.File {
		dstPath := filepath.Join("/etc/zivpn", f.Name)
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(dstPath, f.Mode())
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
			log.Printf("mkdir fail: %v", err)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			log.Printf("open inside zip fail: %v", err)
			continue
		}
		outf, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			log.Printf("create file fail: %v", err)
			continue
		}
		_, _ = io.Copy(outf, rc)
		outf.Close()
		rc.Close()
	}
	_ = os.Remove(temp)
	_ = restartAll()
	jsonResponse(w, http.StatusOK, true, "Restore done", nil)
}

func listBackups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
		return
	}
	out, err := exec.Command("rclone", "lsjson", RcloneRemote).Output()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Rclone lsjson failed: "+err.Error(), nil)
		return
	}
	var files []map[string]interface{}
	if err := json.Unmarshal(out, &files); err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Invalid rclone output: "+err.Error(), nil)
		return
	}
	var res []map[string]interface{}
	for _, it := range files {
		nameRaw, ok := it["Name"]
		if !ok {
			continue
		}
		name, ok := nameRaw.(string)
		if !ok {
			continue
		}
		if strings.HasSuffix(name, ".zip") {
			id := strings.TrimSuffix(name, ".zip")
			res = append(res, map[string]interface{}{
				"id":       id,
				"filename": name,
				"size":     it["Size"],
			})
		}
	}
	jsonResponse(w, http.StatusOK, true, "OK", res)
}

func cleanupOldBackups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, http.StatusMethodNotAllowed, false, "Method not allowed", nil)
		return
	}
	out, err := exec.Command("rclone", "lsjson", RcloneRemote).Output()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Rclone lsjson failed: "+err.Error(), nil)
		return
	}
	var files []map[string]interface{}
	if err := json.Unmarshal(out, &files); err != nil {
		jsonResponse(w, http.StatusInternalServerError, false, "Invalid rclone output: "+err.Error(), nil)
		return
	}
	now := time.Now()
	deleted := 0
	for _, it := range files {
		nameRaw, ok := it["Name"]
		if !ok {
			continue
		}
		name, ok := nameRaw.(string)
		if !ok || !strings.HasSuffix(name, ".zip") {
			continue
		}
		modRaw, ok := it["ModTime"]
		if !ok {
			continue
		}
		modStr, ok := modRaw.(string)
		if !ok {
			continue
		}
		tm, err := time.Parse(time.RFC3339, modStr)
		if err != nil {
			continue
		}
		if now.Sub(tm).Hours() > 24*7 {
			_ = exec.Command("rclone", "delete", RcloneRemote+"/"+name).Run()
			deleted++
		}
	}
	jsonResponse(w, http.StatusOK, true, "Cleanup OK", map[string]int{"deleted": deleted})
}

// file helpers
func loadConfig() (Config, error) {
	var cfg Config
	b, err := ioutil.ReadFile(ConfigFile)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func saveConfig(cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return ioutil.WriteFile(ConfigFile, b, 0644)
}

func loadUsers() ([]string, error) {
	b, err := ioutil.ReadFile(UserDB)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	var out []string
	for _, x := range lines {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out, nil
}

func saveUsers(lines []string) error {
	return ioutil.WriteFile(UserDB, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func appendToFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

func restartAll() error {
	_ = exec.Command("systemctl", "restart", "zivpn").Run()
	_ = exec.Command("systemctl", "restart", "zivpn-api").Run()
	_ = exec.Command("systemctl", "restart", "zivpn-bot").Run()
	return nil
}

func createZip(dest string, paths []string) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	for _, p := range paths {
		if stat, err := os.Stat(p); err == nil && !stat.IsDir() {
			if err := addFileToZip(zw, p, filepath.Base(p)); err != nil {
				log.Printf("addFileToZip fail: %v", err)
			}
		}
	}
	return nil
}

func addFileToZip(zw *zip.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.Method = zip.Deflate

	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	return err
}