package main

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

type TgUpdate struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

func main() {
	botToken := os.Getenv("BOT_TOKEN")
	pdKey := os.Getenv("PIXELDRAIN_KEY")

	if botToken == "" {
		fmt.Println("❌ ERROR FATAL: BOT_TOKEN kosong! Pastikan di Secrets udah bener isinya.")
	} else {
		sensorToken := botToken
		if len(botToken) > 10 {
			sensorToken = botToken[:5] + "..." + botToken[len(botToken)-5:]
		}
		fmt.Printf("🔑 Token Bot Terbaca: %s\n", sensorToken)
	}

	fmt.Println("🚀 Bot CCTV Aktif di Render, siap narik file...")
	go startDummyServer()

	offset := 0
	client := &http.Client{Timeout: 60 * time.Second}

	for {
		apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates?offset=%d&timeout=30", botToken, offset)
		resp, err := client.Get(apiURL)
		
		if err != nil {
			fmt.Printf("⚠️ KONEKSI KE TELEGRAM PUTUS: %v\n", err)
			time.Sleep(3 * time.Second)
			continue
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		var result struct {
			Ok          bool       `json:"ok"`
			ErrorCode   int        `json:"error_code"`
			Description string     `json:"description"`
			Result      []TgUpdate `json:"result"`
		}

		if err := json.Unmarshal(bodyBytes, &result); err != nil {
			time.Sleep(3 * time.Second)
			continue
		}

		if !result.Ok {
			fmt.Printf("❌ TELEGRAM NOLAK REQUEST! Kode: %d | Alasan: %s\n", result.ErrorCode, result.Description)
			time.Sleep(5 * time.Second)
			continue
		}

		for _, update := range result.Result {
			offset = update.UpdateID + 1
			if update.Message != nil && update.Message.Text != "" {
				fmt.Printf("📩 PESAN MASUK: %s\n", update.Message.Text)
				go handleMessage(botToken, pdKey, update.Message.Chat.ID, update.Message.MessageID, update.Message.Text)
			}
		}
	}
}

func handleMessage(token, pdKey string, chatID int64, msgID int, text string) {
	inputURL := strings.TrimSpace(text)

	if inputURL == "/start" || inputURL == "/help" {
		sendTgMessage(token, chatID, msgID, "Halo bro! Kirim aja link download SourceForge ke gw, nanti gw mirror langsung ke PixelDrain. Gas!")
		return
	}

	if !strings.Contains(inputURL, "sourceforge.net") {
		sendTgMessage(token, chatID, msgID, "⚠️ Sori bro, kirim link SourceForge yang valid ya!")
		return
	}

	statusMsgID := sendTgMessage(token, chatID, msgID, "⏳ Memproses link... Menghubungkan ke SourceForge...")

	sfClient := &http.Client{Timeout: 0}
	req, _ := http.NewRequest("GET", inputURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	sfResp, err := sfClient.Do(req)
	if err != nil || sfResp.StatusCode != 200 {
		editTgMessage(token, chatID, statusMsgID, "❌ Gagal nembak link SourceForge. Pastikan filenya ada.")
		return
	}
	defer sfResp.Body.Close()

	filename := "sf_transfer.zip"
	cd := sfResp.Header.Get("Content-Disposition")
	if cd != "" {
		re := regexp.MustCompile(`filename="?([^"]+)"?`)
		matches := re.FindStringSubmatch(cd)
		if len(matches) > 1 {
			filename = matches[1]
		}
	} else {
		u, _ := url.Parse(inputURL)
		parsedName := path.Base(u.Path)
		if parsedName != "" && parsedName != "." {
			filename = parsedName
		}
	}

	editTgMessage(token, chatID, statusMsgID, fmt.Sprintf("📥 **Filename:** `%s`\n⚡ Sedang men-stream langsung ke PixelDrain...", filename))

	pipeReader, pipeWriter := io.Pipe()
	boundaryWriter := multipart.NewWriter(pipeWriter)

	go func() {
		defer pipeWriter.Close()
		defer boundaryWriter.Close()
		part, err := boundaryWriter.CreateFormFile("file", filename)
		if err != nil {
			return
		}
		_, _ = io.Copy(part, sfResp.Body)
	}()

	pdReq, _ := http.NewRequest("POST", "https://pixeldrain.com/api/file", pipeReader)
	pdReq.Header.Set("Content-Type", boundaryWriter.FormDataContentType())
	pdReq.SetBasicAuth("", pdKey)

	pdClient := &http.Client{Timeout: 0}
	pdResp, err := pdClient.Do(pdReq)
	if err != nil {
		editTgMessage(token, chatID, statusMsgID, fmt.Sprintf("❌ Gagal upload ke PixelDrain: %v", err))
		return
	}
	defer pdResp.Body.Close()

	var pdResult map[string]interface{}
	if err := json.NewDecoder(pdResp.Body).Decode(&pdResult); err == nil && (pdResp.StatusCode == 201 || pdResp.StatusCode == 200) {
		fileID := pdResult["id"].(string)
		successText := fmt.Sprintf("✅ **MIRROR SUKSES, BRO!**\n\n📁 **File:** `%s`\n🔗 **Link:** https://pixeldrain.com/u/%s", filename, fileID)
		editTgMessage(token, chatID, statusMsgID, successText)
	} else {
		editTgMessage(token, chatID, statusMsgID, fmt.Sprintf("❌ PixelDrain menolak upload. Status: %d", pdResp.StatusCode))
	}
}

func sendTgMessage(token string, chatID int64, replyID int, text string) int {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	val := url.Values{
		"chat_id":             {fmt.Sprintf("%d", chatID)},
		"text":                {text},
		"parse_mode":          {"Markdown"},
		"reply_to_message_id": {fmt.Sprintf("%d", replyID)},
	}
	resp, err := http.PostForm(apiURL, val)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	
	bodyBytes, _ := io.ReadAll(resp.Body)
	var res map[string]interface{}
	json.Unmarshal(bodyBytes, &res)
	
	if res != nil && res["ok"] != nil && res["ok"].(bool) {
		result := res["result"].(map[string]interface{})
		return int(result["message_id"].(float64))
	}
	return 0
}

func editTgMessage(token string, chatID int64, msgID int, text string) {
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/editMessageText", token)
	val := url.Values{
		"chat_id":    {fmt.Sprintf("%d", chatID)},
		"message_id": {fmt.Sprintf("%d", msgID)},
		"text":       {text},
		"parse_mode": {"Markdown"},
	}
	_, _ = http.PostForm(apiURL, val)
}

func startDummyServer() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Render pakai port 8080 secara default
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Bot is Live!")
	})
	_ = http.ListenAndServe(":"+port, nil)
}
