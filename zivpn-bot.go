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

var ApiKey = "GeekzBot-agskjgdvsbdreiWG1234512SDKrqw"

type BotConfig struct {
	BotToken string `json:"bot_token"`
	AdminID  int64  `json:"admin_id"`
}

type IpInfo struct {
	City string `json:"city"`
	Isp  string `json:"isp"`
}

type UserData struct {
	Password string `json:"password"`
	Expired  string `json:"expired"`
	Status   string `json:"status"`
}

type BackupInfo struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Date     string `json:"date"`
	Domain   string `json:"domain"`
	Size     int64  `json:"size"`
	Note     string `json:"note"`
}

var userStates = make(map[int64]string)
var tempUserData = make(map[int64]map[string]string)
var lastMessageIDs = make(map[int64]int)

func main() {
	if keyBytes, err := ioutil.ReadFile(ApiKeyFile); err == nil {
		ApiKey = strings.TrimSpace(string(keyBytes))
	}
	config, err := loadConfig()
	if err != nil {
		log.Fatal("Gagal memuat konfigurasi bot:", err)
	}

	bot, err := tgbotapi.NewBotAPI(config.BotToken)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = false
	log.Printf("Authorized on account %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			handleMessage(bot, update.Message, config.AdminID)
		} else if update.CallbackQuery != nil {
			handleCallback(bot, update.CallbackQuery, config.AdminID)
		}
	}
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, adminID int64) {
	if msg.From.ID != adminID {
		reply := tgbotapi.NewMessage(msg.Chat.ID, "⛔ Akses Ditolak. Anda bukan admin.")
		sendAndTrack(bot, reply)
		return
	}

	state, exists := userStates[msg.From.ID]
	if exists {
		handleState(bot, msg, state)
		return
	}

	if msg.IsCommand() {
		switch msg.Command() {
		case "start":
			showMainMenu(bot, msg.Chat.ID)
		case "menu":
			showMainMenu(bot, msg.Chat.ID)
		case "backup":
			createBackup(bot, msg.Chat.ID)
		case "restore":
			userStates[msg.From.ID] = "restore_id"
			sendMessage(bot, msg.Chat.ID, "♻ Masukkan ID Restore:")
		case "listbackup":
			listBackups(bot, msg.Chat.ID)
		default:
			msg := tgbotapi.NewMessage(msg.Chat.ID, "Perintah tidak dikenal. Gunakan /menu")
			sendAndTrack(bot, msg)
		}
	}
}

func handleCallback(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery, adminID int64) {
	if query.From.ID != adminID {
		bot.Request(tgbotapi.NewCallback(query.ID, "Akses Ditolak"))
		return
	}

	switch {
	case query.Data == "menu_create":
		userStates[query.From.ID] = "create_username"
		tempUserData[query.From.ID] = make(map[string]string)
		sendMessage(bot, query.Message.Chat.ID, "👤 Masukkan Password:")
	case query.Data == "menu_trial":
		createTrialUser(bot, query.Message.Chat.ID)
	case query.Data == "menu_delete":
		showUserSelection(bot, query.Message.Chat.ID, 1, "delete")
	case query.Data == "menu_renew":
		showUserSelection(bot, query.Message.Chat.ID, 1, "renew")
	case query.Data == "menu_list":
		listUsers(bot, query.Message.Chat.ID)
	case query.Data == "menu_info":
		systemInfo(bot, query.Message.Chat.ID)
	case query.Data == "menu_backup":
		showBackupMenu(bot, query.Message.Chat.ID)
	case query.Data == "backup_create":
		createBackup(bot, query.Message.Chat.ID)
	case query.Data == "backup_list":
		listBackups(bot, query.Message.Chat.ID)
	case query.Data == "backup_restore":
		userStates[query.From.ID] = "restore_id"
		sendMessage(bot, query.Message.Chat.ID, "♻ Masukkan ID Restore:")
	case query.Data == "backup_auto":
		toggleAutoBackup(bot, query.Message.Chat.ID)
	case query.Data == "cancel":
		delete(userStates, query.From.ID)
		delete(tempUserData, query.From.ID)
		showMainMenu(bot, query.Message.Chat.ID)
	case strings.HasPrefix(query.Data, "page_"):
		parts := strings.Split(query.Data, ":")
		action := parts[0][5:]
		page, _ := strconv.Atoi(parts[1])
		showUserSelection(bot, query.Message.Chat.ID, page, action)
	case strings.HasPrefix(query.Data, "select_renew:"):
		username := strings.TrimPrefix(query.Data, "select_renew:")
		tempUserData[query.From.ID] = map[string]string{"username": username}
		userStates[query.From.ID] = "renew_days"
		sendMessage(bot, query.Message.Chat.ID, fmt.Sprintf("🔄 Renewing %s\n⏳ Masukkan Tambahan Durasi (hari):", username))
	case strings.HasPrefix(query.Data, "select_delete:"):
		username := strings.TrimPrefix(query.Data, "select_delete:")
		msg := tgbotapi.NewMessage(query.Message.Chat.ID, fmt.Sprintf("❓ Yakin ingin menghapus user `%s`?", username))
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(
				tgbotapi.NewInlineKeyboardButtonData("✅ Ya, Hapus", "confirm_delete:"+username),
				tgbotapi.NewInlineKeyboardButtonData("❌ Batal", "cancel"),
			),
		)
		sendAndTrack(bot, msg)
	case strings.HasPrefix(query.Data, "confirm_delete:"):
		username := strings.TrimPrefix(query.Data, "confirm_delete:")
		deleteUser(bot, query.Message.Chat.ID, username)
	}

	bot.Request(tgbotapi.NewCallback(query.ID, ""))
}

func handleState(bot *tgbotapi.BotAPI, msg *tgbotapi.Message, state string) {
	userID := msg.From.ID
	text := strings.TrimSpace(msg.Text)

	switch state {
	case "create_username":
		tempUserData[userID]["username"] = text
		userStates[userID] = "create_days"
		sendMessage(bot, msg.Chat.ID, "⏳ Masukkan Durasi (hari):")

	case "create_days":
		days, err := strconv.Atoi(text)
		if err != nil {
			sendMessage(bot, msg.Chat.ID, "❌ Durasi harus angka. Coba lagi:")
			return
		}
		createUser(bot, msg.Chat.ID, tempUserData[userID]["username"], days)
		resetState(userID)

	case "renew_days":
		days, err := strconv.Atoi(text)
		if err != nil {
			sendMessage(bot, msg.Chat.ID, "❌ Durasi harus angka. Coba lagi:")
			return
		}
		renewUser(bot, msg.Chat.ID, tempUserData[userID]["username"], days)
		resetState(userID)
	
	case "restore_id":
		restoreBackup(bot, msg.Chat.ID, text)
		resetState(userID)
	}
}

func createTrialUser(bot *tgbotapi.BotAPI, chatID int64) {
	rand.Seed(time.Now().UnixNano())
	username := fmt.Sprintf("TRIAL%d", 1000+rand.Intn(9000))
	
	res, err := apiCall("POST", "/user/create", map[string]interface{}{
		"password": username,
		"days":     1,
	})

	if err != nil {
		sendMessage(bot, chatID, "❌ Error API: "+err.Error())
		return
	}

	if res["success"] == true {
		data := res["data"].(map[string]interface{})
		
		ipInfo, err := getIpInfo()
		
		msg := fmt.Sprintf("```\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n        ACCOUNT TRIAL ZIVPN UDP\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\nPassword       : %s\nDurasi         : 1 Hari\n",
			data["password"])
		
		if err == nil {
			msg += fmt.Sprintf("CITY           : %s\nISP            : %s\n", ipInfo.City, ipInfo.Isp)
		}
		
		msg += fmt.Sprintf("Domain         : %s\nExpired On     : %s\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n```",
			data["domain"], data["expired"])
		
		reply := tgbotapi.NewMessage(chatID, msg)
		reply.ParseMode = "Markdown"
		deleteLastMessage(bot, chatID)
		bot.Send(reply)
		showMainMenu(bot, chatID)
	} else {
		sendMessage(bot, chatID, fmt.Sprintf("❌ Gagal: %s", res["message"]))
		showMainMenu(bot, chatID)
	}
}

func createBackup(bot *tgbotapi.BotAPI, chatID int64) {
    sendMessage(bot, chatID, "🔄 Membuat backup...")

    res, err := apiCall("POST", "/backup", nil)
    if err != nil {
        sendMessage(bot, chatID, "❌ Error menghubungi API: "+err.Error())
        return
    }

    if success, ok := res["success"].(bool); ok && success {
        data := res["data"].(map[string]interface{})

        msg := "📦 *BACKUP BERHASIL*\n\n"

        if backupID, ok := data["backup_id"].(string); ok {
            msg += fmt.Sprintf("🆔 Backup ID: `%s`\n", backupID)
        }

        if filename, ok := data["filename"].(string); ok {
            msg += fmt.Sprintf("📁 File: `%s`\n", filename)
        }

        msg += "\nGunakan ID ini untuk restore."

        reply := tgbotapi.NewMessage(chatID, msg)
        reply.ParseMode = "Markdown"
        sendAndTrack(bot, reply)
        return
    }

    sendMessage(bot, chatID, "❌ Gagal membuat backup")
}

func listBackups(bot *tgbotapi.BotAPI, chatID int64) {
    sendMessage(bot, chatID, "🔄 Mengambil daftar backup...")

    res, err := apiCall("GET", "/backup/list", nil)
    if err != nil {
        sendMessage(bot, chatID, "❌ Error menghubungi API: "+err.Error())
        return
    }

    if success, _ := res["success"].(bool); !success {
        sendMessage(bot, chatID, "❌ Gagal mengambil daftar backup")
        return
    }

    data, ok := res["data"].([]interface{})
    if !ok || len(data) == 0 {
        sendMessage(bot, chatID, "📭 Tidak ada backup")
        return
    }

    msg := "📋 *Daftar Backup*\n\n"

    for i, item := range data {
        b := item.(map[string]interface{})

        id := b["id"].(string)
        filename := b["filename"].(string)

        msg += fmt.Sprintf(
            "%d. 🆔 `%s`\n   📁 `%s`\n\n",
            i+1, id, filename,
        )
    }

    msg += "Gunakan /restore <ID> untuk restore."

    reply := tgbotapi.NewMessage(chatID, msg)
    reply.ParseMode = "Markdown"
    sendAndTrack(bot, reply)
}

func restoreBackup(bot *tgbotapi.BotAPI, chatID int64, backupID string) {
    sendMessage(bot, chatID, fmt.Sprintf("🔄 Restore backup ID: `%s`...", backupID))

    payload := map[string]interface{}{"backup_id": backupID}

    res, err := apiCall("POST", "/restore", payload)
    if err != nil {
        sendMessage(bot, chatID, "❌ Error API: "+err.Error())
        return
    }

    if success, ok := res["success"].(bool); ok && success {
        msg := fmt.Sprintf(
            "✅ *RESTORE BERHASIL*\n\n🆔 `%s`\n\nServer berhasil direstore.",
            backupID,
        )
        reply := tgbotapi.NewMessage(chatID, msg)
        reply.ParseMode = "Markdown"
        sendAndTrack(bot, reply)
        return
    }

    sendMessage(bot, chatID, fmt.Sprintf("❌ Gagal restore: %v", res["message"]))
}

func showBackupMenu(bot *tgbotapi.BotAPI, chatID int64) {
    msgText := "🔧 *ZiVPN Backup Manager*\n\nPilih menu:"

    msg := tgbotapi.NewMessage(chatID, msgText)
    msg.ParseMode = "Markdown"

    keyboard := tgbotapi.NewInlineKeyboardMarkup(
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("📦 Backup Sekarang", "backup_create"),
            tgbotapi.NewInlineKeyboardButtonData("♻ Restore", "backup_restore"),
        ),
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("⏰ Auto Backup", "backup_auto"),
            tgbotapi.NewInlineKeyboardButtonData("📋 List Backup", "backup_list"),
        ),
        tgbotapi.NewInlineKeyboardRow(
            tgbotapi.NewInlineKeyboardButtonData("🏠 Menu Utama", "cancel"),
        ),
    )
    msg.ReplyMarkup = keyboard
    sendAndTrack(bot, msg)
}

func toggleAutoBackup(bot *tgbotapi.BotAPI, chatID int64) {
    sendMessage(bot, chatID, "🔄 Mengubah auto backup...")

    res, err := apiCall("POST", "/backup/auto", nil)
    if err != nil {
        sendMessage(bot, chatID, "❌ Error menghubungi API: "+err.Error())
        return
    }

    if success, ok := res["success"].(bool); ok && success {
        if data, ok := res["data"].(map[string]interface{}); ok {
            if status, ok := data["status"].(string); ok {
                var msg string
                if status == "enabled" {
                    msg = "✅ *Auto Backup Diaktifkan*"
                    if schedule, ok := data["schedule"].(string); ok {
                        msg += fmt.Sprintf("\nJadwal: `%s`", schedule)
                    }
                } else {
                    msg = "⏸️ *Auto Backup Dinonaktifkan*"
                }

                reply := tgbotapi.NewMessage(chatID, msg)
                reply.ParseMode = "Markdown"
                bot.Send(reply)
                return
            }
        }
    }

    // fallback jika gagal
    if message, ok := res["message"].(string); ok {
        sendMessage(bot, chatID, fmt.Sprintf("❌ Gagal: %s", message))
    } else {
        sendMessage(bot, chatID, "❌ Gagal mengubah auto backup")
    }
}

func showUserSelection(bot *tgbotapi.BotAPI, chatID int64, page int, action string) {
	users, err := getUsers()
	if err != nil {
		sendMessage(bot, chatID, "❌ Gagal mengambil data user: "+err.Error())
		return
	}

	if len(users) == 0 {
		sendMessage(bot, chatID, "📂 Tidak ada user.")
		return
	}

	perPage := 10
	totalPages := (len(users) + perPage - 1) / perPage

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
		password := ""
		if p, ok := u["password"].(string); ok {
			password = p
		}
		
		status := "Active"
		if s, ok := u["status"].(string); ok {
			status = s
		}
		
		expired := ""
		if e, ok := u["expired"].(string); ok {
			expired = e
		}
		
		// Buat label dengan expired date
		label := fmt.Sprintf("%s (%s)", password, expired)
		if status == "Expired" {
			label = fmt.Sprintf("🔴 %s", label)
		} else {
			label = fmt.Sprintf("🟢 %s", label)
		}
		
		if len(label) > 30 {
			label = label[:27] + "..."
		}
		
		data := fmt.Sprintf("select_%s:%s", action, password)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, data),
		))
	}

	var navRow []tgbotapi.InlineKeyboardButton
	if page > 1 {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("⬅️ Prev", fmt.Sprintf("page_%s:%d", action, page-1)))
	}
	if page < totalPages {
		navRow = append(navRow, tgbotapi.NewInlineKeyboardButtonData("Next ➡️", fmt.Sprintf("page_%s:%d", action, page+1)))
	}
	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}
	
	rows = append(rows, tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("❌ Batal", "cancel")))

	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("📋 Pilih User untuk %s (Halaman %d/%d):", strings.Title(action), page, totalPages))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	sendAndTrack(bot, msg)
}

func showMainMenu(bot *tgbotapi.BotAPI, chatID int64) {
	ipInfo, err := getIpInfo()
	domain := "Unknown"
	
	if res, err := apiCall("GET", "/info", nil); err == nil && res["success"] == true {
		if data, ok := res["data"].(map[string]interface{}); ok {
			if d, ok := data["domain"].(string); ok {
				domain = d
			}
		}
	}

	msgText := fmt.Sprintf("```\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n           MENU ZIVPN UDP\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n • Domain   : %s\n",
		domain)
	
	if err == nil {
		msgText += fmt.Sprintf(" • City     : %s\n • ISP      : %s\n", ipInfo.City, ipInfo.Isp)
	}
	
	msgText += "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n```"

	msg := tgbotapi.NewMessage(chatID, msgText)
	msg.ParseMode = "Markdown"

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("👤 Create Password", "menu_create"),
			tgbotapi.NewInlineKeyboardButtonData("🎯 Trial 1 Hari", "menu_trial"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🗑️ Delete Password", "menu_delete"),
			tgbotapi.NewInlineKeyboardButtonData("🔄 Renew Password", "menu_renew"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📋 List Passwords", "menu_list"),
			tgbotapi.NewInlineKeyboardButtonData("📊 System Info", "menu_info"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("💾 Backup/Restore", "menu_backup"),
		),
	)
	msg.ReplyMarkup = keyboard
	sendAndTrack(bot, msg)
}

func sendMessage(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if _, inState := userStates[chatID]; inState {
		cancelKb := tgbotapi.NewInlineKeyboardMarkup(
			tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData("❌ Batal", "cancel")),
		)
		msg.ReplyMarkup = cancelKb
	}
	sendAndTrack(bot, msg)
}

func resetState(userID int64) {
	delete(userStates, userID)
	delete(tempUserData, userID)
}

func deleteLastMessage(bot *tgbotapi.BotAPI, chatID int64) {
    if msgID, ok := lastMessageIDs[chatID]; ok {
        deleteMsg := tgbotapi.NewDeleteMessage(chatID, msgID)
        _, _ = bot.Request(deleteMsg)
        delete(lastMessageIDs, chatID)
    }
}

func sendAndTrack(bot *tgbotapi.BotAPI, msg tgbotapi.MessageConfig) {
    if msgID, ok := lastMessageIDs[msg.ChatID]; ok {
        deleteMsg := tgbotapi.NewDeleteMessage(msg.ChatID, msgID)
        _, _ = bot.Request(deleteMsg)
        delete(lastMessageIDs, msg.ChatID)
    }

    sentMsg, err := bot.Send(msg)
    if err != nil {
        log.Printf("Error sending message: %v", err)
        return
    }

    lastMessageIDs[msg.ChatID] = sentMsg.MessageID
}

func apiCall(method, endpoint string, payload interface{}) (map[string]interface{}, error) {
    var reqBody []byte
    var err error

    if payload != nil {
        reqBody, err = json.Marshal(payload)
        if err != nil {
            log.Printf("API Marshal error: %v", err)
            return nil, err
        }
    }

    client := &http.Client{Timeout: 30 * time.Second}
    req, err := http.NewRequest(method, ApiUrl+endpoint, bytes.NewBuffer(reqBody))
    if err != nil {
        log.Printf("API Request error: %v", err)
        return nil, err
    }

    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("X-API-Key", ApiKey)

    log.Printf("API Calling: %s %s", method, ApiUrl+endpoint)
    
    resp, err := client.Do(req)
    if err != nil {
        log.Printf("API Do error: %v", err)
        return nil, err
    }
    defer resp.Body.Close()

    body, _ := ioutil.ReadAll(resp.Body)
    log.Printf("API Raw Response (%s %s): %s", method, endpoint, string(body))
    
    var result map[string]interface{}
    if err := json.Unmarshal(body, &result); err != nil {
        log.Printf("API JSON unmarshal error: %v | Body: %s", err, string(body))
        return nil, err
    }

    log.Printf("API Response %s %s: %v", method, endpoint, result)
    return result, nil
}

func getIpInfo() (IpInfo, error) {
	resp, err := http.Get("http://ip-api.com/json/")
	if err != nil {
		return IpInfo{}, err
	}
	defer resp.Body.Close()

	var info IpInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return IpInfo{}, err
	}
	return info, nil
}

func getUsers() ([]map[string]interface{}, error) {
	res, err := apiCall("GET", "/users", nil)
	if err != nil {
		return nil, err
	}

	log.Printf("getUsers response: %+v", res)

	if success, ok := res["success"].(bool); ok && success {
		if data, ok := res["data"].([]interface{}); ok {
			var users []map[string]interface{}
			for _, item := range data {
				if user, ok := item.(map[string]interface{}); ok {
					users = append(users, user)
				}
			}
			return users, nil
		}
	}
	
	message := "unknown error"
	if msg, ok := res["message"].(string); ok {
		message = msg
	}
	return nil, fmt.Errorf("failed to get users: %s", message)
}

func createUser(bot *tgbotapi.BotAPI, chatID int64, username string, days int) {
	res, err := apiCall("POST", "/user/create", map[string]interface{}{
		"password": username,
		"days":     days,
	})

	if err != nil {
		sendMessage(bot, chatID, "❌ Error API: "+err.Error())
		return
	}

	if res["success"] == true {
		data := res["data"].(map[string]interface{})
		
		ipInfo, err := getIpInfo()
		
		msg := fmt.Sprintf("```\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n         ACCOUNT ZIVPN UDP\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\nPassword       : %s\n",
			data["password"])
		
		if err == nil {
			msg += fmt.Sprintf("CITY           : %s\nISP            : %s\n", ipInfo.City, ipInfo.Isp)
		}
		
		msg += fmt.Sprintf("Domain         : %s\nExpired On     : %s\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n```",
			data["domain"], data["expired"])
		
		reply := tgbotapi.NewMessage(chatID, msg)
		reply.ParseMode = "Markdown"
		deleteLastMessage(bot, chatID)
		bot.Send(reply)
		showMainMenu(bot, chatID)
	} else {
		sendMessage(bot, chatID, fmt.Sprintf("❌ Gagal: %s", res["message"]))
		showMainMenu(bot, chatID)
	}
}

func deleteUser(bot *tgbotapi.BotAPI, chatID int64, username string) {
	res, err := apiCall("POST", "/user/delete", map[string]interface{}{
		"password": username,
	})

	if err != nil {
		sendMessage(bot, chatID, "❌ Error API: "+err.Error())
		return
	}

	if res["success"] == true {
		msg := tgbotapi.NewMessage(chatID, "✅ Password berhasil dihapus.")
		deleteLastMessage(bot, chatID)
		bot.Send(msg)
		showMainMenu(bot, chatID)
	} else {
		sendMessage(bot, chatID, fmt.Sprintf("❌ Gagal: %s", res["message"]))
		showMainMenu(bot, chatID)
	}
}

func renewUser(bot *tgbotapi.BotAPI, chatID int64, username string, days int) {
	res, err := apiCall("POST", "/user/renew", map[string]interface{}{
		"password": username,
		"days":     days,
	})

	if err != nil {
		sendMessage(bot, chatID, "❌ Error API: "+err.Error())
		return
	}

	if res["success"] == true {
		data := res["data"].(map[string]interface{})
		
		ipInfo, err := getIpInfo()

		domain := "Unknown"
		if d, ok := data["domain"].(string); ok && d != "" {
			domain = d
		} else {
			if infoRes, err := apiCall("GET", "/info", nil); err == nil && infoRes["success"] == true {
				if infoData, ok := infoRes["data"].(map[string]interface{}); ok {
					if d, ok := infoData["domain"].(string); ok {
						domain = d
					}
				}
			}
		}

		msg := fmt.Sprintf("```\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n         ACCOUNT ZIVPN UDP\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\nPassword       : %s\n",
			data["password"])
		
		if err == nil {
			msg += fmt.Sprintf("CITY           : %s\nISP            : %s\n", ipInfo.City, ipInfo.Isp)
		}
		
		msg += fmt.Sprintf("Domain         : %s\nExpired On     : %s\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n```",
			domain, data["expired"])
		
		reply := tgbotapi.NewMessage(chatID, msg)
		reply.ParseMode = "Markdown"
		deleteLastMessage(bot, chatID)
		bot.Send(reply)
		showMainMenu(bot, chatID)
	} else {
		sendMessage(bot, chatID, fmt.Sprintf("❌ Gagal: %s", res["message"]))
		showMainMenu(bot, chatID)
	}
}

func listUsers(bot *tgbotapi.BotAPI, chatID int64) {
    res, err := apiCall("GET", "/users", nil)
    if err != nil {
        sendMessage(bot, chatID, "❌ Error API: "+err.Error())
        return
    }

    if success, ok := res["success"].(bool); ok && success {
        data, ok := res["data"].([]interface{})
        if !ok {
            sendMessage(bot, chatID, "📂 Format data tidak valid.")
            return
        }

        if len(data) == 0 {
            sendMessage(bot, chatID, "📂 Tidak ada user.")
            return
        }

        msg := "📋 *List Passwords*\n\n"

        for i, u := range data {
            user, ok := u.(map[string]interface{})
            if !ok {
                continue
            }

            statusIcon := "🟢"
            if status, ok := user["status"].(string); ok && status == "Expired" {
                statusIcon = "🔴"
            }

            password := ""
            if p, ok := user["password"].(string); ok {
                password = p
            }

            expired := ""
            if e, ok := user["expired"].(string); ok {
                expired = e
            }

            if password == "" || expired == "" {
                continue
            }

            msg += fmt.Sprintf(
                "%d. %s `%s`\n   Expired: `%s`\n\n",
                i+1, statusIcon, password, expired,
            )
        }

        msg += fmt.Sprintf("📊 Total: %d user(s)", len(data))

        reply := tgbotapi.NewMessage(chatID, msg)
        reply.ParseMode = "Markdown"

        reply.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
            tgbotapi.NewInlineKeyboardRow(
                tgbotapi.NewInlineKeyboardButtonData("🔙 Kembali", "cancel"),
            ),
        )

        sendAndTrack(bot, reply)
        return
    }

    message := "❌ Gagal mengambil data."
    if msg, ok := res["message"].(string); ok {
        message = "❌ " + msg
    }

    sendMessage(bot, chatID, message)
}

func systemInfo(bot *tgbotapi.BotAPI, chatID int64) {
	res, err := apiCall("GET", "/info", nil)
	if err != nil {
		sendMessage(bot, chatID, "❌ Error API: "+err.Error())
		return
	}

	if res["success"] != true {
		sendMessage(bot, chatID, "❌ Gagal mengambil info.")
		return
	}

	data := res["data"].(map[string]interface{})
	ipInfo, _ := getIpInfo()

	msg := "```\n"
	msg += "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"
	msg += "          SERVER INFORMATION\n"
	msg += "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"

	add := func(label string, key string) {
		if val, ok := data[key].(string); ok {
			msg += fmt.Sprintf("%-14s : %s\n", label, val)
		}
	}

	add("IP", "public_ip")
	add("Domain", "domain")
	add("OS", "os")
	add("Kernel", "kernel")
	add("CPU", "cpu")
	add("Cores", "cores")
	add("RAM", "ram")
	add("Disk", "disk")
	add("Port", "port")
	add("Service", "service")

	if ipInfo.City != "" {
		msg += fmt.Sprintf("%-14s : %s\n", "City", ipInfo.City)
		msg += fmt.Sprintf("%-14s : %s\n", "ISP", ipInfo.Isp)
	}

	if serverTime, ok := data["server_time"].(string); ok {
		msg += fmt.Sprintf("%-14s : %s\n", "Time", serverTime)
	}

	if backupCount, ok := data["backup_count"].(float64); ok {
		msg += fmt.Sprintf("%-14s : %.0f\n", "Backups", backupCount)
	}

	msg += "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n"
	msg += "```"

	reply := tgbotapi.NewMessage(chatID, msg)
	reply.ParseMode = "Markdown"

	reply.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔙 Kembali", "cancel"),
		),
	)

	deleteLastMessage(bot, chatID)
	bot.Send(reply)
}

func loadConfig() (BotConfig, error) {
	var config BotConfig
	file, err := ioutil.ReadFile(BotConfigFile)
	if err != nil {
		return config, err
	}
	err = json.Unmarshal(file, &config)
	return config, err
}