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
	ConfigFile       = "/etc/zivpn/config.json"
	UserDB           = "/etc/zivpn/users.db"
	DomainFile       = "/etc/zivpn/domain"
	ApiKeyFile       = "/etc/zivpn/apikey"
	BackupDir        = "/etc/zivpn/backups"
	RcloneRemote     = "drive:ZIVPN-BACKUP"
	Port             = ":8080"
)

var AuthToken = ""

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

var mutex = &sync.Mutex{}
var backupMutex = &sync.Mutex{}

func main() {
	if keyBytes, err := ioutil.ReadFile(ApiKeyFile); err == nil {
		AuthToken = strings.TrimSpace(string(keyBytes))
	}
	os.MkdirAll(BackupDir, 0755)

	http.HandleFunc("/api/user/create", authMiddleware(createUser))
	http.HandleFunc("/api/user/delete", authMiddleware(deleteUser))
	http.HandleFunc("/api/user/renew", authMiddleware(renewUser))
	http.HandleFunc("/api/users", authMiddleware(listUsers))
	http.HandleFunc("/api/info", authMiddleware(getSystemInfo))
	http.HandleFunc("/api/backup", authMiddleware(handleBackup))
	http.HandleFunc("/api/backup/list", authMiddleware(listBackups))
	http.HandleFunc("/api/restore", authMiddleware(handleRestore))
	http.HandleFunc("/api/backup/cleanup", authMiddleware(cleanupOldBackups))

	log.Fatal(http.ListenAndServe(Port, nil))
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != AuthToken {
			jsonResponse(w, 401, false, "Unauthorized", nil)
			return
		}
		next(w, r)
	}
}

func jsonResponse(w http.ResponseWriter, status int, success bool, message string, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(Response{Success: success, Message: message, Data: data})
}

func generateBackupID() string {
	charset := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 16)
	for i := range b {
		b[i] = charset[r.Intn(len(charset))]
	}
	return string(b)
}

func createUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, 405, false, "Method not allowed", nil)
		return
	}
	var req UserRequest
	json.NewDecoder(r.Body).Decode(&req)
	if req.Password == "" || req.Days <= 0 {
		jsonResponse(w, 400, false, "Invalid request", nil)
		return
	}
	mutex.Lock()
	defer mutex.Unlock()

	config, err := loadConfig()
	if err != nil {
		jsonResponse(w, 500, false, "Config read error", nil)
		return
	}
	for _, u := range config.Auth.Config {
		if u == req.Password {
			jsonResponse(w, 409, false, "User exists", nil)
			return
		}
	}
	config.Auth.Config = append(config.Auth.Config, req.Password)
	saveConfig(config)

	exp := time.Now().Add(time.Duration(req.Days) * 24 * time.Hour).Format("2006-01-02")
	entry := fmt.Sprintf("%s | %s\n", req.Password, exp)
	f, _ := os.OpenFile(UserDB, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	f.WriteString(entry)
	f.Close()
	restartAll()

	jsonResponse(w, 200, true, "User created", map[string]string{
		"password": req.Password,
		"expired":  exp,
	})
}

func deleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, 405, false, "Method not allowed", nil)
		return
	}
	var req UserRequest
	json.NewDecoder(r.Body).Decode(&req)

	mutex.Lock()
	defer mutex.Unlock()

	config, _ := loadConfig()
	var newList []string
	found := false
	for _, u := range config.Auth.Config {
		if u == req.Password {
			found = true
		} else {
			newList = append(newList, u)
		}
	}
	if !found {
		jsonResponse(w, 404, false, "User not found", nil)
		return
	}
	config.Auth.Config = newList
	saveConfig(config)

	users, _ := loadUsers()
	var newUsers []string
	for _, line := range users {
		if strings.HasPrefix(line, req.Password+" ") {
			continue
		}
		newUsers = append(newUsers, line)
	}
	saveUsers(newUsers)

	restartAll()
	jsonResponse(w, 200, true, "User deleted", nil)
}

func renewUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, 405, false, "Method not allowed", nil)
		return
	}
	var req UserRequest
	json.NewDecoder(r.Body).Decode(&req)

	mutex.Lock()
	defer mutex.Unlock()

	users, _ := loadUsers()
	found := false
	var newUsers []string
	for _, line := range users {
		parts := strings.Split(line, "|")
		if strings.TrimSpace(parts[0]) == req.Password {
			found = true
			old, _ := time.Parse("2006-01-02", strings.TrimSpace(parts[1]))
			if old.Before(time.Now()) {
				old = time.Now()
			}
			newExp := old.Add(time.Duration(req.Days) * 24 * time.Hour).Format("2006-01-02")
			newUsers = append(newUsers, fmt.Sprintf("%s | %s", req.Password, newExp))
		} else {
			newUsers = append(newUsers, line)
		}
	}
	if !found {
		jsonResponse(w, 404, false, "User not found", nil)
		return
	}
	saveUsers(newUsers)
	restartAll()
	jsonResponse(w, 200, true, "User renewed", nil)
}

func listUsers(w http.ResponseWriter, r *http.Request) {
	users, _ := loadUsers()
	var list []map[string]string
	for _, line := range users {
		parts := strings.Split(line, "|")
		p := strings.TrimSpace(parts[0])
		e := strings.TrimSpace(parts[1])
		st := "Active"
		if e < time.Now().Format("2006-01-02") {
			st = "Expired"
		}
		list = append(list, map[string]string{
			"password": p,
			"expired":  e,
			"status":   st,
		})
	}
	jsonResponse(w, 200, true, "OK", list)
}

func getSystemInfo(w http.ResponseWriter, r *http.Request) {
	ipPub, _ := exec.Command("curl", "-s", "ifconfig.me").Output()
	ipPriv, _ := exec.Command("hostname", "-I").Output()
	domain := "Unknown"
	if b, err := ioutil.ReadFile(DomainFile); err == nil {
		domain = strings.TrimSpace(string(b))
	}
	jsonResponse(w, 200, true, "OK", map[string]string{
		"public_ip":  strings.TrimSpace(string(ipPub)),
		"private_ip": strings.Fields(string(ipPriv))[0],
		"domain":     domain,
	})
}

func handleBackup(w http.ResponseWriter, r *http.Request) {
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

	f, _ := os.Create(tempFile)
	wr := zip.NewWriter(f)

	for _, file := range files {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			continue
		}
		src, _ := os.Open(file)
		info, _ := src.Stat()
		header, _ := zip.FileInfoHeader(info)
		header.Name = filepath.Base(file)
		dst, _ := wr.CreateHeader(header)
		io.Copy(dst, src)
		src.Close()
	}
	wr.Close()
	f.Close()

	exec.Command("rclone", "copy", tempFile, RcloneRemote).Run()
	os.Remove(tempFile)

	jsonResponse(w, 200, true, "Backup success", map[string]string{
		"backup_id": id,
		"filename":  filename,
	})
}

func handleRestore(w http.ResponseWriter, r *http.Request) {
	var req BackupRequest
	json.NewDecoder(r.Body).Decode(&req)
	if req.BackupID == "" {
		jsonResponse(w, 400, false, "Missing backup ID", nil)
		return
	}

	temp := filepath.Join("/tmp", req.BackupID+".zip")
	exec.Command("rclone", "copy", RcloneRemote+"/"+req.BackupID+".zip", "/tmp/").Run()

	z, err := zip.OpenReader(temp)
	if err != nil {
		jsonResponse(w, 500, false, "Backup not found", nil)
		return
	}
	for _, f := range z.File {
		path := filepath.Join("/etc/zivpn", f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.Mode())
			continue
		}
		rc, _ := f.Open()
		dst, _ := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		io.Copy(dst, rc)
		dst.Close()
		rc.Close()
	}
	z.Close()
	os.Remove(temp)
	restartAll()

	jsonResponse(w, 200, true, "Restore done", nil)
}

func listBackups(w http.ResponseWriter, r *http.Request) {
	out, _ := exec.Command("rclone", "lsjson", RcloneRemote).Output()
	var list []map[string]interface{}
	json.Unmarshal(out, &list)

	var result []map[string]interface{}
	for _, x := range list {
		name := x["Name"].(string)
		if strings.HasSuffix(name, ".zip") {
			id := strings.TrimSuffix(name, ".zip")
			result = append(result, map[string]interface{}{
				"id":       id,
				"filename": name,
				"size":     x["Size"],
			})
		}
	}
	jsonResponse(w, 200, true, "OK", result)
}

func cleanupOldBackups(w http.ResponseWriter, r *http.Request) {
	out, _ := exec.Command("rclone", "lsjson", RcloneRemote).Output()
	var list []map[string]interface{}
	json.Unmarshal(out, &list)

	now := time.Now()
	count := 0

	for _, x := range list {
		name := x["Name"].(string)
		mod := x["ModTime"].(string)
		t, _ := time.Parse(time.RFC3339, mod)
		if now.Sub(t).Hours() > 24*7 {
			exec.Command("rclone", "delete", RcloneRemote+"/"+name).Run()
			count++
		}
	}
	jsonResponse(w, 200, true, "Cleanup OK", map[string]int{"deleted": count})
}

func loadConfig() (Config, error) {
	var cfg Config
	b, err := ioutil.ReadFile(ConfigFile)
	if err != nil {
		return cfg, err
	}
	json.Unmarshal(b, &cfg)
	return cfg, nil
}

func saveConfig(cfg Config) error {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return ioutil.WriteFile(ConfigFile, b, 0644)
}

func loadUsers() ([]string, error) {
	b, err := ioutil.ReadFile(UserDB)
	if err != nil {
		return []string{}, nil
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

func restartAll() {
	exec.Command("systemctl", "restart", "zivpn").Run()
	exec.Command("systemctl", "restart", "zivpn-api").Run()
	exec.Command("systemctl", "restart", "zivpn-bot").Run()
}