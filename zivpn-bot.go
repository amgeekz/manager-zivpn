package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	BotConfigFile = "/etc/zivpn/bot-config.json"
	ApiUrl        = "http://127.0.0.1:8080/api"
	ApiKeyFile    = "/etc/zivpn/apikey"
)

var ApiKey = ""

type BotConfig struct {
	BotToken string `json:"bot_token"`
	AdminID  int64  `json:"admin_id"`
}

type IpInfo struct {
	City string `json:"city"`
	Isp  string `json:"isp"`
}

var userStates = make(map[int64]string)
var tempUserData = make(map[int64]map[string]string)
var lastMessageIDs = make(map[int64]int)

func main() {
	if b, err := ioutil.ReadFile(ApiKeyFile); err == nil {
		ApiKey = strings.TrimSpace(string(b))
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		log.Fatal(err)
	}
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		if update.Message != nil {
			if update.Message.From.ID != cfg.AdminID {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, "🚫 *AKSES DITOLAK*\n\n_Hanya admin yang dapat mengakses bot ini._")
				msg.ParseMode = "Markdown"
				bot.Send(msg)
				continue
			}
			handleMessage(bot, update.Message, cfg.AdminID)
		} else if update.CallbackQuery != nil {
			if update.CallbackQuery.From.ID != cfg.AdminID {
				bot.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, "Akses Ditolak"))
				continue
			}
			handleCallback(bot, update.CallbackQuery, cfg.AdminID)
		}
	}
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, adminID int64) {
	if state, ok := userStates[msg.From.ID]; ok && state != "" {
		handleState(bot, msg, state)
		return
	}
	if msg.IsCommand() {
		switch msg.Command() {
		case "start", "menu":
			showMainMenu(bot, msg.Chat.ID)
		case "backup":
			createBackup(bot, msg.Chat.ID)
		case "restore":
			userStates[msg.From.ID] = "restore_id"
			tempUserData[msg.From.ID] = make(map[string]string)
			sendMessageSimple(bot, msg.Chat.ID, "🔄 *RESTORE BACKUP*\n\nSilakan masukkan **ID Backup**:")
		case "listbackup":
			listBackups(bot, msg.Chat.ID)
		default:
			sendMessageSimple(bot, msg.Chat.ID, "❌ *PERINTAH TIDAK DIKENAL*\n\nGunakan `/menu` untuk membuka menu utama.")
		}
	}
}

func handleCallback(bot *tgbotapi.BotAPI, q *tgbotapi.CallbackQuery, adminID int64) {
	data := q.Data
	bot.Request(tgbotapi.NewCallback(q.ID, ""))
	switch {
	case data == "menu_create":
		userStates[q.From.ID] = "create_username"
		tempUserData[q.From.ID] = make(map[string]string)
		sendMessageSimple(bot, q.Message.Chat.ID, "👤 *BUAT AKUN BARU*\n\nSilakan masukkan **username** untuk akun baru:")
	case data == "menu_trial":
		createTrialUser(bot, q.Message.Chat.ID)
	case data == "menu_delete":
		showUserSelection(bot, q.Message.Chat.ID, 1, "delete")
	case data == "menu_renew":
		showUserSelection(bot, q.Message.Chat.ID, 1, "renew")
	case data == "menu_list":
		listUsers(bot, q.Message.Chat.ID)
	case data == "menu_info":
		systemInfo(bot, q.Message.Chat.ID)
	case data == "menu_backup":
		showBackupMenu(bot, q.Message.Chat.ID)
	case data == "backup_create":
		createBackup(bot, q.Message.Chat.ID)
	case data == "backup_list":
		listBackups(bot, q.Message.Chat.ID)
	case data == "backup_restore":
		userStates[q.From.ID] = "restore_id"
		tempUserData[q.From.ID] = make(map[string]string)
		sendMessageSimple(bot, q.Message.Chat.ID, "🔄 *RESTORE BACKUP*\n\nSilakan masukkan **ID Backup**:")
	case data == "backup_auto":
		toggleAutoBackup(bot, q.Message.Chat.ID)
	case data == "cancel":
		resetState(q.From.ID)
		showMainMenu(bot, q.Message.Chat.ID)
	case strings.HasPrefix(data, "page_"):
		parts := strings.Split(data, ":")
		if len(parts) == 2 {
			action := strings.TrimPrefix(parts[0], "page_")
			page, _ := strconv.Atoi(parts[1])
			showUserSelection(bot, q.Message.Chat.ID, page, action)
		}
	case strings.HasPrefix(data, "select_renew:"):
		username := strings.TrimPrefix(data, "select_renew:")
		tempUserData[q.From.ID] = map[string]string{"username": username}
		userStates[q.From.ID] = "renew_days"
		sendMessageSimple(bot, q.Message.Chat.ID, fmt.Sprintf("🔄 *RENEW AKUN*\n\nUsername: `%s`\n\nMasukkan **jumlah hari** untuk diperpanjang:", username))
	case strings.HasPrefix(data, "select_delete:"):
		username := strings.TrimPrefix(data, "select_delete:")
		msg := tgbotapi.NewMessage(q.Message.Chat.ID, fmt.Sprintf("🗑 *KONFIRMASI HAPUS*\n\nApakah Anda yakin ingin menghapus akun:\n\n`%s`", username))
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ Ya, Hapus", "confirm_delete:"+username),
				tgbotapi.NewInlineKeyboardButtonData("❌ Batal", "cancel"),
			),
		)
		sendAndTrack(bot, msg)
	case strings.HasPrefix(data, "confirm_delete:"):
		username := strings.TrimPrefix(data, "confirm_delete:")
		deleteUser(bot, q.Message.Chat.ID, username)
	}
}

func handleState(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, state string) {
	uid := msg.From.ID
	text := strings.TrimSpace(msg.Text)
	switch state {
	case "create_username":
		if tempUserData[uid] == nil {
			tempUserData[uid] = make(map[string]string)
		}
		tempUserData[uid]["username"] = text
		userStates[uid] = "create_days"
		sendMessageSimple(bot, msg.Chat.ID, "⏳ *DURASI AKUN*\n\nMasukkan **jumlah hari** masa aktif akun:")
	case "create_days":
		days, err := strconv.Atoi(text)
		if err != nil || days <= 0 {
			sendMessageSimple(bot, msg.Chat.ID, "❌ *INPUT TIDAK VALID*\n\nDurasi harus berupa **angka lebih dari 0**.")
			return
		}
		username := tempUserData[uid]["username"]
		createUser(bot, msg.Chat.ID, username, days)
		resetState(uid)
	case "renew_days":
		days, err := strconv.Atoi(text)
		if err != nil || days <= 0 {
			sendMessageSimple(bot, msg.Chat.ID, "❌ *INPUT TIDAK VALID*\n\nDurasi harus berupa **angka lebih dari 0**.")
			return
		}
		username := tempUserData[uid]["username"]
		renewUser(bot, msg.Chat.ID, username, days)
		resetState(uid)
	case "restore_id":
	restoreBackup(bot, msg.Chat.ID, text)
	resetState(uid)
	default:
		resetState(uid)
	}
}

func createTrialUser(bot *tgbotapi.BotAPI, chatID int64) {
	username := fmt.Sprintf("TRIAL%d", 1000+rand.Intn(9000))

	res, err := apiCall("POST", "/user/create", map[string]interface{}{
		"password": username,
		"days":     1,
	})

	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL MEMBUAT TRIAL*\nError: "+err.Error())
		return
	}

	if success, _ := res["success"].(bool); success {
		data := res["data"].(map[string]interface{})

		domain := fmt.Sprintf("%v", data["domain"])
		if domain == "<nil>" || domain == "" {
			domain = "Unknown"
		}

		msg := fmt.Sprintf(
			"🎯 *AKUN TRIAL BERHASIL DIBUAT*\n\n"+
				"▸ **Username**: `%s`\n"+
				"▸ **Password**: `%s`\n"+
				"▸ **Domain**: `%s`\n"+
				"▸ **Masa Aktif**: 1 Hari\n"+
				"▸ **Expired**: `%s`\n\n"+
				"_Akun trial akan otomatis terhapus setelah expired._",
			data["password"],
			data["password"],
			domain,
			data["expired"],
		)

		reply := tgbotapi.NewMessage(chatID, msg)
		reply.ParseMode = "Markdown"
		bot.Send(reply)

		return
	}
	sendMessageSimple(bot, chatID, "❌ *GAGAL MEMBUAT TRIAL*")
}

func createBackup(bot *tgbotapi.BotAPI, chatID int64) {
	sendMessageSimple(bot, chatID, "🔄 *MEMBUAT BACKUP...*\n\n_Sedang memproses, harap tunggu..._")

	res, err := apiCall("POST", "/backup", nil)
	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL MEMBUAT BACKUP*\n\nError: "+err.Error())
		return
	}

	if success, ok := res["success"].(bool); ok && success {
		data := res["data"].(map[string]interface{})

		backupID := fmt.Sprintf("%v", data["backup_id"])
		filename := fmt.Sprintf("%v", data["filename"])

		msg := fmt.Sprintf(
			"✅ *BACKUP BERHASIL DIBUAT*\n\n"+
				"▸ **Google Drive File ID**: `%s`\n"+
				"▸ **Nama File**: `%s`\n\n"+
				"_Gunakan File ID saat melakukan restore._",
			backupID,
			filename,
		)

		sendMessageSimple(bot, chatID, msg)
		return
	}

	sendMessageSimple(bot, chatID, "❌ *GAGAL MEMBUAT BACKUP*")
}

func listBackups(bot *tgbotapi.BotAPI, chatID int64) {
	sendMessageSimple(bot, chatID, "📋 *MENGAMBIL DAFTAR BACKUP...*")

	res, err := apiCall("GET", "/backup/list", nil)
	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL MENGAMBIL BACKUP*\n\nError: "+err.Error())
		return
	}

	if success, ok := res["success"].(bool); ok && success {
		arr, _ := res["data"].([]interface{})
		if len(arr) == 0 {
			sendMessageSimple(bot, chatID, "📭 *TIDAK ADA BACKUP*\n\n_Belum ada backup yang tersedia._")
			return
		}

		var b strings.Builder
		b.WriteString("📦 *DAFTAR BACKUP (Google Drive)*\n\n")

		for i, it := range arr {
			m := it.(map[string]interface{})
			id := fmt.Sprintf("%v", m["id"]) // sekarang ID = Google Drive File ID
			file := fmt.Sprintf("%v", m["filename"])

			b.WriteString(fmt.Sprintf(
				"**%d.**\n"+
					"▸ *File ID:* `%s`\n"+
					"▸ *File:* `%s`\n\n",
				i+1, id, file,
			))
		}

		sendMessageSimple(bot, chatID, b.String())
		return
	}

	sendMessageSimple(bot, chatID, "❌ *GAGAL MENGAMBIL DAFTAR BACKUP*")
}

func restoreBackup(bot *tgbotapi.BotAPI, chatID int64, backupID string) {
	sendMessageSimple(bot, chatID, 
		fmt.Sprintf("🔄 *MEMPROSES RESTORE...*\n\nID Backup (Drive ID): `%s`", backupID))

	res, err := apiCall("POST", "/restore", map[string]interface{}{
		"backup_id": backupID,
	})

	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL RESTORE*\n\nError: "+err.Error())
		return
	}

	if ok, _ := res["success"].(bool); ok {
		sendMessageSimple(bot, chatID, 
			"✅ *RESTORE BERHASIL*\n\n_Sistem berhasil direstore dari Google Drive._")
		return
	}

	sendMessageSimple(bot, chatID, 
		"❌ *GAGAL RESTORE*\n\nPesan: "+fmt.Sprintf("%v", res["message"]))
}

func showBackupMenu(bot *tgbotapi.BotAPI, chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "💾 *BACKUP MANAGER*\n\n_Pilih opsi backup yang diinginkan:_")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📦 Backup Sekarang", "backup_create"),
			tgbotapi.NewInlineKeyboardButtonData("🔄 Restore", "backup_restore"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⏰ Auto Backup", "backup_auto"),
			tgbotapi.NewInlineKeyboardButtonData("📋 List Backup", "backup_list"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🏠 Menu Utama", "cancel"),
		),
	)
	msg.ParseMode = "Markdown"
	sendAndTrack(bot, msg)
}

func toggleAutoBackup(bot *tgbotapi.BotAPI, chatID int64) {
	sendMessageSimple(bot, chatID, "⚙️ *MENGUBAH SETTING AUTO BACKUP...*")
	res, err := apiCall("POST", "/backup/auto", nil)
	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL MENGUBAH SETTING*\n\nError: "+err.Error())
		return
	}
	if success, ok := res["success"].(bool); ok && success {
		sendMessageSimple(bot, chatID, "✅ *AUTO BACKUP DIPERBARUI*")
		return
	}
	sendMessageSimple(bot, chatID, "❌ *GAGAL MENGUBAH SETTING AUTO BACKUP*")
}

func showUserSelection(bot *tgbotapi.BotAPI, chatID int64, page int, action string) {
	users, err := getUsers()
	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL MENGAMBIL USER*\n\nError: "+err.Error())
		return
	}
	if len(users) == 0 {
		sendMessageSimple(bot, chatID, "📭 *TIDAK ADA USER*\n\n_Belum ada user yang terdaftar._")
		return
	}
	perPage := 8
	totalPages := (len(users)+perPage-1)/perPage
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * perPage
	end := start + perPage
	if end > len(users) {
		end = len(users)
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	for _, u := range users[start:end] {
		pass := fmt.Sprintf("%v", u["password"])
		exp := fmt.Sprintf("%v", u["expired"])
		label := fmt.Sprintf("%s (%s)", pass, exp)
		if len(label) > 34 {
			label = label[:31] + "..."
		}
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("select_%s:%s", action, pass))))
	}
	var nav []tgbotapi.InlineKeyboardButton
	if page > 1 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("⬅️ Prev", fmt.Sprintf("page_%s:%d", action, page-1)))
	}
	if page < totalPages {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("Next ➡️", fmt.Sprintf("page_%s:%d", action, page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("❌ Batal", "cancel")))
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("👥 *PILIH USER UNTUK %s*\n\nHalaman: **%d/%d**", strings.ToUpper(action), page, totalPages))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	msg.ParseMode = "Markdown"
	sendAndTrack(bot, msg)
}

func showMainMenu(bot *tgbotapi.BotAPI, chatID int64) {
	ipInfo, _ := getIpInfo()
	domain := "Unknown"
	if res, err := apiCall("GET", "/info", nil); err == nil {
		if s, ok := res["success"].(bool); ok && s {
			if d, ok := res["data"].(map[string]interface{}); ok {
				if dd, ok := d["domain"].(string); ok && dd != "" {
					domain = dd
				}
			}
		}
	}
	msgText := fmt.Sprintf("🚀 *ZIVPN CONTROL PANEL*\n\n"+
		"▸ **Domain**: `%s`\n", domain)
	if ipInfo.City != "" {
		msgText += fmt.Sprintf("▸ **Lokasi**: %s\n▸ **ISP**: %s\n", ipInfo.City, ipInfo.Isp)
	}
	msgText += "\n_Pilih menu di bawah untuk melanjutkan:_"
	msg := tgbotapi.NewMessage(chatID, msgText)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("👤 Buat Akun", "menu_create"),
			tgbotapi.NewInlineKeyboardButtonData("🎯 Akun Trial", "menu_trial"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🗑 Hapus Akun", "menu_delete"),
			tgbotapi.NewInlineKeyboardButtonData("🔄 Renew Akun", "menu_renew"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📋 List User", "menu_list"),
			tgbotapi.NewInlineKeyboardButtonData("📊 Info System", "menu_info"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💾 Backup", "menu_backup"),
		),
	)
	sendAndTrack(bot, msg)
}

func sendMessageSimple(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if _, ok := userStates[chatID]; ok {
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("❌ Batal", "cancel")))
	}
	sendAndTrack(bot, msg)
}

func resetState(uid int64) {
	delete(userStates, uid)
	delete(tempUserData, uid)
}

func deleteLastMessage(bot *tgbotapi.BotAPI, chatID int64) {
	if id, ok := lastMessageIDs[chatID]; ok {
		_, _ = bot.Request(tgbotapi.NewDeleteMessage(chatID, id))
		delete(lastMessageIDs, chatID)
	}
}

func sendAndTrack(bot *tgbotapi.BotAPI, msg tgbotapi.MessageConfig) {
	if mid, ok := lastMessageIDs[msg.ChatID]; ok {
		_, _ = bot.Request(tgbotapi.NewDeleteMessage(msg.ChatID, mid))
		delete(lastMessageIDs, msg.ChatID)
	}

	sent, err := bot.Send(msg)
	if err != nil {
		log.Printf("send error: %v", err)
		return
	}

	lastMessageIDs[msg.ChatID] = sent.MessageID
}

func apiCall(method, endpoint string, payload interface{}) (map[string]interface{}, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(method, ApiUrl+endpoint, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ApiKey != "" {
		req.Header.Set("X-API-Key", ApiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := ioutil.ReadAll(resp.Body)
	var res map[string]interface{}
	if err := json.Unmarshal(b, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func getIpInfo() (IpInfo, error) {
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get("http://ip-api.com/json/")
	if err != nil {
		return IpInfo{}, err
	}
	defer resp.Body.Close()
	var i IpInfo
	if err := json.NewDecoder(resp.Body).Decode(&i); err != nil {
		return IpInfo{}, err
	}
	return i, nil
}

func getUsers() ([]map[string]interface{}, error) {
	res, err := apiCall("GET", "/users", nil)
	if err != nil {
		return nil, err
	}
	if ok, _ := res["success"].(bool); !ok {
		msg := fmt.Sprintf("%v", res["message"])
		return nil, fmt.Errorf(msg)
	}
	arr, _ := res["data"].([]interface{})
	var out []map[string]interface{}
	for _, it := range arr {
		if m, ok := it.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func listUsers(bot *tgbotapi.BotAPI, chatID int64) {
	users, err := getUsers()
	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL MENGAMBIL USER*\n\nError: "+err.Error())
		return
	}

	if len(users) == 0 {
		sendMessageSimple(bot, chatID, "📭 *TIDAK ADA USER*\n\n_Belum ada user yang terdaftar._")
		return
	}

	var b strings.Builder
	b.WriteString("📋 *DAFTAR USER AKTIF*\n\n")

	for i, u := range users {
		pass := fmt.Sprintf("%v", u["password"])
		exp := fmt.Sprintf("%v", u["expired"])
		status := fmt.Sprintf("%v", u["status"])

		icon := "🟢"
		if status == "Expired" {
			icon = "🔴"
		}

		b.WriteString(fmt.Sprintf("**%d.** %s `%s`\n   ▸ Expired: `%s`\n\n", i+1, icon, pass, exp))
	}

	msg := tgbotapi.NewMessage(chatID, b.String())
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Kembali ke Menu", "cancel"),
		),
	)

	sendAndTrack(bot, msg)
}

func createUser(bot *tgbotapi.BotAPI, chatID int64, username string, days int) {
	res, err := apiCall("POST", "/user/create", map[string]interface{}{
		"password": username,
		"days":     days,
	})

	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL MEMBUAT AKUN*\nError: "+err.Error())
		return
	}

	if ok, _ := res["success"].(bool); ok {
		data := res["data"].(map[string]interface{})

		domain := fmt.Sprintf("%v", data["domain"])
		if domain == "<nil>" || domain == "" {
			domain = "Unknown"
		}

		msg := fmt.Sprintf(
			"✅ *AKUN BERHASIL DIBUAT*\n\n"+
				"▸ **Username**: `%v`\n"+
				"▸ **Password**: `%v`\n"+
				"▸ **Domain**: `%s`\n"+
				"▸ **Expired**: `%v`\n\n"+
				"_Simpan informasi akun dengan baik._",
			data["password"],
			data["password"],
			domain,
			data["expired"],
		)

		reply := tgbotapi.NewMessage(chatID, msg)
		reply.ParseMode = "Markdown"
		bot.Send(reply)

		return
	}

	sendMessageSimple(bot, chatID, "❌ *GAGAL MEMBUAT AKUN*: "+fmt.Sprintf("%v", res["message"]))
}

func deleteUser(bot *tgbotapi.BotAPI, chatID int64, username string) {
	sendMessageSimple(bot, chatID, "🗑 *MENGHAPUS AKUN...*\n\nUsername: `"+username+"`")
	res, err := apiCall("POST", "/user/delete", map[string]interface{}{"password": username})
	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL MENGHAPUS*\n\nError: "+err.Error())
		return
	}
	if ok, _ := res["success"].(bool); ok {
		sendMessageSimple(bot, chatID, "✅ *AKUN BERHASIL DIHAPUS*\n\nUsername: `"+username+"`")
		showMainMenu(bot, chatID)
		return
	}
	sendMessageSimple(bot, chatID, "❌ *GAGAL MENGHAPUS*\n\nPesan: "+fmt.Sprintf("%v", res["message"]))
	showMainMenu(bot, chatID)
}

func renewUser(bot *tgbotapi.BotAPI, chatID int64, username string, days int) {
	sendMessageSimple(bot, chatID, "🔄 *MEMPERPANJANG AKUN...*\n\nUsername: `"+username+"`")
	res, err := apiCall("POST", "/user/renew", map[string]interface{}{"password": username, "days": days})
	if err != nil {
		sendMessageSimple(bot, chatID, "❌ *GAGAL RENEW*\n\nError: "+err.Error())
		return
	}
	if ok, _ := res["success"].(bool); ok {
		data := res["data"].(map[string]interface{})
		msg := fmt.Sprintf("✅ *AKUN BERHASIL DIPERPANJANG*\n\n"+
			"▸ **Username**: `%v`\n"+
			"▸ **Expired Baru**: `%v`\n\n"+
			"_Akun telah diperpanjang selama %d hari._",
			data["password"], data["expired"], days)
		sendMessageSimple(bot, chatID, msg)
		showMainMenu(bot, chatID)
		return
	}
	sendMessageSimple(bot, chatID, "❌ *GAGAL RENEW*\n\nPesan: "+fmt.Sprintf("%v", res["message"]))
	showMainMenu(bot, chatID)
}

func systemInfo(bot *tgbotapi.BotAPI, chatID int64) {
	res, err := apiCall("GET", "/info", nil)
	if err != nil {
		sendMessageSimple(bot, chatID, "❌ Gagal mengambil info: "+err.Error())
		return
	}

	data := res["data"].(map[string]interface{})
	get := func(k string) string { return strings.TrimSpace(fmt.Sprintf("%v", data[k])) }

	separator := "━━━━━━━━━━━━━━━━━━━━"

	msg := fmt.Sprintf(
		"*🖥️ VPS INFORMATION*\n%s\n"+
			"🌍 *IP Address* : `%s`\n"+
			"🔗 *Domain*     : `%s`\n"+
			"🧩 *OS*         : `%s`\n"+
			"🧬 *Kernel*     : `%s`\n"+
			"💠 *CPU*        : `%s`\n"+
			"⚙️ *Cores*      : `%s`\n"+
			"📦 *RAM*        : `%s`\n"+
			"💽 *Disk*       : `%s`\n"+
			"⏱ *Uptime*     : `%s`\n"+
			"🛰 *Service*    : `%s`\n"+
			"👥 *Active Users* : `%s`\n"+
			"🗂 *Backups*    : `%s`\n"+
			"⏰ *Server Time* : `%s`\n%s",
		separator,
		get("public_ip"),
		get("domain"),
		get("os"),
		get("kernel"),
		get("cpu"),
		get("cores"),
		get("ram"),
		get("disk"),
		get("uptime"),
		get("service"),
		get("user_count"),
		get("backup_count"),
		get("server_time"),
		separator,
	)

	m := tgbotapi.NewMessage(chatID, msg)
	m.ParseMode = "Markdown"
	m.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Kembali", "cancel"),
		),
	)
	sendAndTrack(bot, m)
}

func loadConfig() (BotConfig, error) {
	var cfg BotConfig
	b, err := ioutil.ReadFile(BotConfigFile)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}