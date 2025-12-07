package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	ConfigFile     = "/etc/zivpn/config.json"
	UserDB         = "/etc/zivpn/users.db"
	DomainFile     = "/etc/zivpn/domain"
	ApiKeyFile     = "/etc/zivpn/apikey"
	BackupDir      = "/etc/zivpn/backups"
	RcloneRemote   = "drive:ZIVPN-BACKUP"
	Port           = ":8080"
	AutoBackupFile = "/etc/zivpn/backup_auto.json"
)

var (
	AuthToken   = ""
	mutex       = &sync.Mutex{}
	backupMutex = &sync.Mutex{}
)

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

type AutoBackupCfg struct {
	Enabled  bool   `json:"enabled"`
	Schedule string `json:"schedule"`
}

type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func jsonResponse(w http.ResponseWriter, status int, success bool, message string, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Response{Success: success, Message: message, Data: data})
}

func generateBackupID() string {
	const c = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, 12)
	for i := range b {
		b[i] = c[r.Intn(len(c))]
	}
	return string(b)
}

func loadConfig() (Config, error) {
	var cfg Config
	b, err := ioutil.ReadFile(ConfigFile)
	if err != nil {
		return cfg, err
	}
	_ = json.Unmarshal(b, &cfg)
	return cfg, nil
}

func saveConfig(cfg Config) error {
	b, _ := json.MarshalIndent(cfg, "", "  ")
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
	out := []string{}
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

func restartAll() {
    go func() {
        cmd := exec.Command("systemctl", "restart", "zivpn.service")
        cmd.Stdout = io.Discard
        cmd.Stderr = io.Discard
        _ = cmd.Run()
    }()
}

func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if AuthToken != "" && r.Header.Get("X-API-Key") != AuthToken {
			jsonResponse(w, 401, false, "Unauthorized", nil)
			return
		}
		next(w, r)
	}
}

func getDomain() string {
    b, err := ioutil.ReadFile(DomainFile)
    if err != nil {
        return "Unknown"
    }
    return strings.TrimSpace(string(b))
}

func createUserHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonResponse(w, 405, false, "Method not allowed", nil)
		return
	}
	var req UserRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Password == "" || req.Days <= 0 {
		jsonResponse(w, 400, false, "Invalid request", nil)
		return
	}

	mutex.Lock()
	defer mutex.Unlock()

	cfg, err := loadConfig()
	if err != nil {
		jsonResponse(w, 500, false, "Read config error", nil)
		return
	}

	for _, u := range cfg.Auth.Config {
		if u == req.Password {
			jsonResponse(w, 409, false, "User exists", nil)
			return
		}
	}

	cfg.Auth.Config = append(cfg.Auth.Config, req.Password)
	_ = saveConfig(cfg)

	exp := time.Now().Add(24 * time.Hour * time.Duration(req.Days)).Format("2006-01-02")
	_ = appendToFile(UserDB, fmt.Sprintf("%s | %s\n", req.Password, exp))

	go restartAll()

	jsonResponse(w, 200, true, "User created", map[string]string{
	"password": req.Password,
	"expired": exp,
	"domain": getDomain(), 
  })
}

func deleteUserHandler(w http.ResponseWriter, r *http.Request) {
	var req UserRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Password == "" {
		jsonResponse(w, 400, false, "Invalid request", nil)
		return
	}

	mutex.Lock()
	defer mutex.Unlock()

	cfg, _ := loadConfig()
	var newAuth []string
	found := false

	for _, u := range cfg.Auth.Config {
		if u == req.Password {
			found = true
			continue
		}
		newAuth = append(newAuth, u)
	}

	if !found {
		jsonResponse(w, 404, false, "User not found", nil)
		return
	}

	cfg.Auth.Config = newAuth
	_ = saveConfig(cfg)

	users, _ := loadUsers()
	var newUsers []string
	for _, line := range users {
		if !strings.HasPrefix(line, req.Password+" ") &&
			!strings.HasPrefix(line, req.Password+"|") {
			newUsers = append(newUsers, line)
		}
	}
	_ = saveUsers(newUsers)

	go restartAll()
	jsonResponse(w, 200, true, "User deleted", nil)
}

func renewUserHandler(w http.ResponseWriter, r *http.Request) {
	var req UserRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.Password == "" || req.Days <= 0 {
		jsonResponse(w, 400, false, "Invalid request", nil)
		return
	}

	mutex.Lock()
	defer mutex.Unlock()

	users, _ := loadUsers()
	found := false
	var newUsers []string
	var newExp string

	for _, line := range users {
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			newUsers = append(newUsers, line)
			continue
		}

		pass := strings.TrimSpace(parts[0])
		exp := strings.TrimSpace(parts[1])

		if pass == req.Password {
			found = true

			old, err := time.Parse("2006-01-02", exp)
			if err != nil || old.Before(time.Now()) {
				old = time.Now()
			}

			newExp = old.Add(time.Hour * 24 * time.Duration(req.Days)).Format("2006-01-02")
			newUsers = append(newUsers, fmt.Sprintf("%s | %s", pass, newExp))

		} else {
			newUsers = append(newUsers, line)
		}
	}

	if !found {
		jsonResponse(w, 404, false, "User not found", nil)
		return
	}

	_ = saveUsers(newUsers)
	go restartAll()

	jsonResponse(w, 200, true, "User renewed", map[string]string{
		"password": req.Password,
		"expired":  newExp,
		"domain":   getDomain(),
	})
}

func listUsersHandler(w http.ResponseWriter, r *http.Request) {
	users, _ := loadUsers()
	today := time.Now().Format("2006-01-02")

	var out []map[string]string
	for _, l := range users {
		parts := strings.Split(l, "|")
		if len(parts) < 2 {
			continue
		}
		p := strings.TrimSpace(parts[0])
		e := strings.TrimSpace(parts[1])
		st := "Active"
		if e < today {
			st = "Expired"
		}
		out = append(out, map[string]string{
			"password": p,
			"expired":  e,
			"status":   st,
		})
	}

	jsonResponse(w, 200, true, "OK", out)
}

func execOut(cmd string) string {
	out, err := exec.Command("bash", "-c", cmd).CombinedOutput()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	lines := strings.Split(s, "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return ""
}

func isActive(service string) bool {
	out, err := exec.Command("systemctl", "is-active", service).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "active"
}

func getSystemInfoHandler(w http.ResponseWriter, r *http.Request) {
	pub := execOut(`curl -s ifconfig.me`)
	priv := strings.TrimSpace(execOut(`hostname -I | awk '{print $1}'`))

	domain := "Unknown"
	if b, err := ioutil.ReadFile(DomainFile); err == nil {
		domain = strings.TrimSpace(string(b))
	}

	osInfo := strings.TrimSpace(execOut(`grep PRETTY_NAME /etc/os-release | cut -d= -f2 | tr -d '"'`))
	if osInfo == "" {
		osInfo = execOut(`uname -a`)
	}

	kernel := execOut(`uname -r`)

	cpu := execOut(`grep "model name" /proc/cpuinfo | head -1 | cut -d: -f2`)
	cpu = strings.TrimSpace(cpu)

	cores := execOut(`nproc`)

	ramUsed := execOut(`free -m | awk '/Mem:/ {print $3}'`)
	ramTotal := execOut(`free -m | awk '/Mem:/ {print $2}'`)
	ram := fmt.Sprintf("%sMB / %sMB", strings.TrimSpace(ramUsed), strings.TrimSpace(ramTotal))

	diskUsed := execOut(`df -h / | awk 'NR==2 {print $3}'`)
	diskAvail := execOut(`df -h / | awk 'NR==2 {print $4}'`)
	disk := fmt.Sprintf("%s / %s", strings.TrimSpace(diskUsed), strings.TrimSpace(diskAvail))

	uptime := strings.TrimSpace(execOut(`uptime -p | sed 's/up //'`))

	backupCount := 0
	if out, err := exec.Command("rclone", "lsjson", RcloneRemote).Output(); err == nil {
		var arr []map[string]interface{}
		if json.Unmarshal(out, &arr) == nil {
			for _, x := range arr {
				if n, ok := x["Name"].(string); ok && strings.HasSuffix(n, ".zip") {
					backupCount++
				}
			}
		}
	}

	userCount := 0
	if b, err := ioutil.ReadFile(UserDB); err == nil {
		lines := strings.Split(string(b), "\n")
		now := time.Now().Format("2006-01-02")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || !strings.Contains(line, "|") {
				continue
			}
			parts := strings.Split(line, "|")
			if len(parts) >= 2 {
				exp := strings.TrimSpace(parts[1])
				if exp >= now {
					userCount++
				}
			}
		}
	}

	serviceStatus := "active"
	if !isActive("zivpn") || !isActive("zivpn-api") || !isActive("zivpn-bot") {
		serviceStatus = "inactive"
	}

	jsonResponse(w, 200, true, "OK", map[string]interface{}{
		"public_ip":    pub,
		"private_ip":   priv,
		"domain":       domain,
		"os":           osInfo,
		"kernel":       kernel,
		"cpu":          cpu,
		"cores":        cores,
		"ram":          ram,
		"disk":         disk,
		"uptime":       uptime,
		"port":         "5667 UDP, 8080 API",
		"service":      serviceStatus,
		"server_time":  time.Now().Format("2006-01-02 15:04:05"),
		"backup_count": backupCount,
		"user_count":   userCount,
	})
}

func createZip(dest string, paths []string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	for _, p := range paths {
		if s, err := os.Stat(p); err == nil && !s.IsDir() {
			src, err := os.Open(p)
			if err != nil {
				continue
			}
			header, _ := zip.FileInfoHeader(s)
			header.Name = filepath.Base(p)
			dst, _ := w.CreateHeader(header)
			io.Copy(dst, src)
			src.Close()
		}
	}
	return nil
}

func handleBackupHandler(w http.ResponseWriter, r *http.Request) {
	backupMutex.Lock()
	defer backupMutex.Unlock()

	id := generateBackupID()
	filename := id + ".zip"
	temp := filepath.Join(BackupDir, filename)

	files := []string{
		ConfigFile, UserDB, DomainFile, ApiKeyFile,
		"/etc/zivpn/bot-config.json",
		"/etc/zivpn/zivpn.crt",
		"/etc/zivpn/zivpn.key",
	}

	_ = createZip(temp, files)

	// Upload ke Google Drive
	_, err := exec.Command("rclone", "copy", temp, RcloneRemote).CombinedOutput()
	if err != nil {
		jsonResponse(w, 500, false, "Upload failed", nil)
		return
	}

	// Ambil daftar file untuk mendapatkan FILE ID Google Drive
	list, err := exec.Command("rclone", "lsjson", RcloneRemote).Output()
	if err != nil {
		jsonResponse(w, 500, false, "Failed reading drive list", nil)
		return
	}

	var driveFiles []map[string]interface{}
	json.Unmarshal(list, &driveFiles)

	var fileID string
	for _, f := range driveFiles {
		if f["Name"] == filename {
			if idStr, ok := f["ID"].(string); ok {
				fileID = idStr
			}
		}
	}

	if fileID == "" {
		jsonResponse(w, 500, false, "Unable to detect Google Drive file ID", nil)
		return
	}

	os.Remove(temp)

	jsonResponse(w, 200, true, "Backup success", map[string]string{
		"backup_id": fileID,
		"filename":  filename,
	})
}

func listBackupsHandler(w http.ResponseWriter, r *http.Request) {
	out, err := exec.Command("rclone", "lsjson", RcloneRemote).Output()
	if err != nil {
		jsonResponse(w, 500, false, err.Error(), nil)
		return
	}

	var arr []map[string]interface{}
	_ = json.Unmarshal(out, &arr)

	var res []map[string]interface{}
	for _, f := range arr {
		name, ok := f["Name"].(string)
		if !ok || !strings.HasSuffix(name, ".zip") {
			continue
		}

		fileID := ""
		if idStr, ok := f["ID"].(string); ok {
			fileID = idStr
		}

		res = append(res, map[string]interface{}{
			"id":       fileID,  // FILE ID Google Drive
			"filename": name,
			"size":     f["Size"],
		})
	}

	jsonResponse(w, 200, true, "OK", res)
}

func restoreHandler(w http.ResponseWriter, r *http.Request) {
	var req BackupRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.BackupID == "" {
		jsonResponse(w, 400, false, "Invalid backup ID", nil)
		return
	}

	tmp := "/tmp/restore.zip"

	_, err := exec.Command(
		"rclone",
		"copy",
		fmt.Sprintf("drive:%s", req.BackupID),
		tmp,
		"--drive-id", req.BackupID,
	).CombinedOutput()

	if err != nil {
		jsonResponse(w, 500, false, "Copy failed", nil)
		return
	}

	z, err := zip.OpenReader(tmp)
	if err != nil {
		jsonResponse(w, 500, false, err.Error(), nil)
		return
	}
	defer z.Close()

	for _, f := range z.File {
		dst := filepath.Join("/etc/zivpn", f.Name)
		os.MkdirAll(filepath.Dir(dst), 0755)

		rc, _ := f.Open()
		out, _ := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())

		io.Copy(out, rc)

		out.Close()
		rc.Close()
	}

	_ = os.Remove(tmp)
	go restartAll()

	jsonResponse(w, 200, true, "Restore done", nil)
}

func cleanupOldBackupsHandler(w http.ResponseWriter, r *http.Request) {
	out, err := exec.Command("rclone", "lsjson", RcloneRemote).Output()
	if err != nil {
		jsonResponse(w, 500, false, err.Error(), nil)
		return
	}

	var arr []map[string]interface{}
	json.Unmarshal(out, &arr)

	now := time.Now()
	deleted := 0

	for _, x := range arr {
		n, ok := x["Name"].(string)
		if !ok || !strings.HasSuffix(n, ".zip") {
			continue
		}
		tm, _ := time.Parse(time.RFC3339, x["ModTime"].(string))
		if now.Sub(tm).Hours() > 168 {
			exec.Command("rclone", "delete", RcloneRemote+"/"+n).Run()
			deleted++
		}
	}

	jsonResponse(w, 200, true, "Cleanup OK", map[string]int{
		"deleted": deleted,
	})
}

func toggleAutoBackupHandler(w http.ResponseWriter, r *http.Request) {
	cfg := AutoBackupCfg{Enabled: false, Schedule: "0 2 * * *"}

	if b, err := ioutil.ReadFile(AutoBackupFile); err == nil {
		json.Unmarshal(b, &cfg)
	}

	cfg.Enabled = !cfg.Enabled

	b, _ := json.MarshalIndent(cfg, "", "  ")
	_ = ioutil.WriteFile(AutoBackupFile, b, 0644)

	if cfg.Enabled {
		_ = ioutil.WriteFile("/etc/cron.d/zivpn-backup",
			[]byte(cfg.Schedule+" root /usr/local/bin/zivpn-backup\n"), 0644)
	} else {
		_ = os.Remove("/etc/cron.d/zivpn-backup")
	}

	jsonResponse(w, 200, true, "OK", cfg)
}

func getAutoBackupStatusHandler(w http.ResponseWriter, r *http.Request) {
	cfg := AutoBackupCfg{Enabled: false, Schedule: "0 2 * * *"}
	if b, err := ioutil.ReadFile(AutoBackupFile); err == nil {
		json.Unmarshal(b, &cfg)
	}
	jsonResponse(w, 200, true, "OK", cfg)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	if b, err := ioutil.ReadFile(ApiKeyFile); err == nil {
		AuthToken = strings.TrimSpace(string(b))
	}

	os.MkdirAll(BackupDir, 0755)

	http.HandleFunc("/api/user/create", authMiddleware(createUserHandler))
	http.HandleFunc("/api/user/delete", authMiddleware(deleteUserHandler))
	http.HandleFunc("/api/user/renew", authMiddleware(renewUserHandler))
	http.HandleFunc("/api/users", authMiddleware(listUsersHandler))
	http.HandleFunc("/api/info", authMiddleware(getSystemInfoHandler))
	http.HandleFunc("/api/backup", authMiddleware(handleBackupHandler))
	http.HandleFunc("/api/backup/list", authMiddleware(listBackupsHandler))
	http.HandleFunc("/api/restore", authMiddleware(restoreHandler))
	http.HandleFunc("/api/backup/cleanup", authMiddleware(cleanupOldBackupsHandler))
	http.HandleFunc("/api/backup/auto", authMiddleware(toggleAutoBackupHandler))
	http.HandleFunc("/api/backup/auto/status", authMiddleware(getAutoBackupStatusHandler))

	log.Println("ZiVPN API running on", Port)
	log.Fatal(http.ListenAndServe(Port, nil))
}