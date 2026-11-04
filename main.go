package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/glebarez/go-sqlite"
	_ "github.com/lib/pq"
)

var (
	db        *sql.DB
	session   *discordgo.Session
	botToken  string
	dbDialect = "sqlite"

	setupMu         sync.Mutex
	setupInProgress = map[string]bool{}
	pickBanMu       sync.Mutex
)

// ── Maps ────────────────────────────────────────────────────────────────
type MapInfo struct {
	Name  string
	Emoji string
}

var Maps = []MapInfo{
	{"Sandstone", "🏜️"},
	{"Rust", "🏭"},
	{"Province", "🏘️"},
	{"Breeze", "🏝️"},
	{"Prison", "🏰"},
	{"Hanami", "🌸"},
	{"Dune", "🌅"},
}

// ── Leagues ─────────────────────────────────────────────────────────────
type LeagueCategory struct {
	Name     string
	Emoji    string
	Lobbies  []int
	RoleName string // роль для доступа к лиге
}

var LeagueConfig = []LeagueCategory{
	{"PRO LEAGUE", "🔴", []int{1, 2, 3}, "Pro League"},
	{"PRO REMASTERED", "🔴", []int{4, 5}, "Pro League"},
	{"DIVISION LEAGUE", "🟣", []int{1, 2, 3}, "Division League"},
	{"DIVISION REMASTERED", "🟣", []int{4, 5}, "Division League"},
	{"PROSPECT LEAGUE", "🟢", []int{1, 2, 3}, "Prospect League"},
	{"PROSPECT REMASTERED", "🟢", []int{4, 5}, "Prospect League"},
	{"DEFAULT LEAGUE", "⚪", []int{1, 2, 3}, "Default League"},
	{"DEFAULT REMASTERED", "⚪", []int{4, 5}, "Default League"},
}

var LeagueRoles = []string{"Pro League", "Division League", "Prospect League", "Default League"}

// ── Active pick/ban state ───────────────────────────────────────────────
type PickBanState struct {
	MatchID      int
	GuildID      string
	ChannelID    string
	MessageID    string
	TCapID       string // discord user id of T captain
	CTCapID      string // discord user id of CT captain
	TTeam        []PlayerInfo
	CTTeam       []PlayerInfo
	BannedMaps   []string
	Turn         string // "T" or "CT"
	League       string
	BanVersion   int
	TVoiceID     string
	CTVoiceID    string
	VoiceParentID string
}

type PlayerInfo struct {
	DiscordID string
	Username  string
	UniqueID  int
	IsCaptain bool
}

var activePickBans = map[string]*PickBanState{} // key = channelID

func main() {
	token := os.Getenv("DISCORD_TOKEN")
	if token == "" {
		log.Fatal("DISCORD_TOKEN not set")
	}
	botToken = token

	initDB()

	var err error
	session, err = discordgo.New("Bot " + token)
	if err != nil {
		log.Fatal("Error creating session:", err)
	}

	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsGuildMembers |
		discordgo.IntentsMessageContent

	session.AddHandler(onReady)
	session.AddHandler(onInteraction)
	session.AddHandler(onVoiceStateUpdate)
	session.AddHandler(onGuildMemberAdd)
	session.AddHandler(onMessageCreate)

	err = session.Open()
	if err != nil {
		log.Fatal("Error opening connection:", err)
	}
	defer session.Close()

	// HTTP сервер для Fly.io health check (порт 8080)
	go func() {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("OK"))
		})
		log.Printf("HTTP health server on :%s", port)
		http.ListenAndServe(":"+port, nil)
	}()

	fmt.Println("Bot is running. Press Ctrl+C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc
}

// ── Database ────────────────────────────────────────────────────────────

func sqlize(query string) string {
	if dbDialect != "postgres" {
		return query
	}
	var b strings.Builder
	idx := 1
	for _, ch := range query {
		if ch == '?' {
			b.WriteString(fmt.Sprintf("$%d", idx))
			idx++
		} else {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

func execDB(query string, args ...any) (sql.Result, error) {
	return db.Exec(sqlize(query), args...)
}

func queryRowDB(query string, args ...any) *sql.Row {
	return db.QueryRow(sqlize(query), args...)
}

func queryDB(query string, args ...any) (*sql.Rows, error) {
	return db.Query(sqlize(query), args...)
}

func initDB() {
	var err error

	if databaseURL := os.Getenv("DATABASE_URL"); databaseURL != "" {
		dbDialect = "postgres"
		db, err = sql.Open("postgres", databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		if err = db.Ping(); err != nil {
			log.Fatal(err)
		}
		log.Println("Connected to PostgreSQL via DATABASE_URL")

		_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS users (
			id SERIAL PRIMARY KEY,
			discord_id TEXT UNIQUE NOT NULL,
			username TEXT NOT NULL,
			game_nick TEXT DEFAULT '',
			game_id TEXT DEFAULT '',
			is_registered INTEGER DEFAULT 0,
			elo INTEGER DEFAULT 1000,
			games INTEGER DEFAULT 0,
			wins INTEGER DEFAULT 0,
			losses INTEGER DEFAULT 0,
			kills INTEGER DEFAULT 0,
			deaths INTEGER DEFAULT 0,
			assists INTEGER DEFAULT 0,
			mvps INTEGER DEFAULT 0,
			joined_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`)

		_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS matches (
			id SERIAL PRIMARY KEY,
			status TEXT DEFAULT 'picking',
			map_name TEXT DEFAULT '',
			score_t INTEGER DEFAULT 0,
			score_ct INTEGER DEFAULT 0,
			host_id TEXT DEFAULT '',
			category TEXT DEFAULT '',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`)

		_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS match_players (
			id SERIAL PRIMARY KEY,
			match_id INTEGER,
			user_id INTEGER,
			team TEXT,
			kills INTEGER DEFAULT 0,
			deaths INTEGER DEFAULT 0,
			assists INTEGER DEFAULT 0,
			rating REAL DEFAULT 0,
			is_captain INTEGER DEFAULT 0
		)`)

		_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS map_stats (
			id SERIAL PRIMARY KEY,
			user_id INTEGER,
			map_name TEXT,
			wins INTEGER DEFAULT 0,
			losses INTEGER DEFAULT 0,
			kills INTEGER DEFAULT 0,
			deaths INTEGER DEFAULT 0
		)`)

		_, _ = db.Exec("ALTER TABLE users ADD COLUMN IF NOT EXISTS game_nick TEXT DEFAULT ''")
		_, _ = db.Exec("ALTER TABLE users ADD COLUMN IF NOT EXISTS game_id TEXT DEFAULT ''")
		return
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "/app/data"
	}
	os.MkdirAll(dbPath, 0755)

	dbDialect = "sqlite"
	db, err = sql.Open("sqlite", dbPath+"/bot.db")
	if err != nil {
		log.Fatal(err)
	}

	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		discord_id TEXT UNIQUE NOT NULL,
		username TEXT NOT NULL,
		game_nick TEXT DEFAULT '',
		game_id TEXT DEFAULT '',
		is_registered INTEGER DEFAULT 0,
		elo INTEGER DEFAULT 1000,
		games INTEGER DEFAULT 0,
		wins INTEGER DEFAULT 0,
		losses INTEGER DEFAULT 0,
		kills INTEGER DEFAULT 0,
		deaths INTEGER DEFAULT 0,
		assists INTEGER DEFAULT 0,
		mvps INTEGER DEFAULT 0,
		joined_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)

	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS matches (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		status TEXT DEFAULT 'picking',
		map_name TEXT DEFAULT '',
		score_t INTEGER DEFAULT 0,
		score_ct INTEGER DEFAULT 0,
		host_id TEXT DEFAULT '',
		category TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)

	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS match_players (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		match_id INTEGER,
		user_id INTEGER,
		team TEXT,
		kills INTEGER DEFAULT 0,
		deaths INTEGER DEFAULT 0,
		assists INTEGER DEFAULT 0,
		rating REAL DEFAULT 0,
		is_captain INTEGER DEFAULT 0
	)`)

	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS map_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		map_name TEXT,
		wins INTEGER DEFAULT 0,
		losses INTEGER DEFAULT 0,
		kills INTEGER DEFAULT 0,
		deaths INTEGER DEFAULT 0
	)`)

	_, _ = db.Exec("ALTER TABLE users ADD COLUMN game_nick TEXT DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN game_id TEXT DEFAULT ''")
}

func getOrCreateUser(discordID, username string) (int, error) {
	var id int
	err := queryRowDB("SELECT id FROM users WHERE discord_id = ?", discordID).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := execDB("INSERT INTO users (discord_id, username) VALUES (?, ?)", discordID, username)
		if err != nil {
			return 0, err
		}
		if dbDialect == "postgres" {
			err = queryRowDB("SELECT id FROM users WHERE discord_id = ?", discordID).Scan(&id)
			return id, err
		}
		lid, _ := res.LastInsertId()
		return int(lid), nil
	}
	return id, err
}

// ── Ready ───────────────────────────────────────────────────────────────

func onReady(s *discordgo.Session, r *discordgo.Ready) {
	log.Printf("Logged in as %s", r.User.Username)

	// Удалить все старые глобальные команды
	existingCmds, _ := s.ApplicationCommands(r.User.ID, "")
	for _, cmd := range existingCmds {
		_ = s.ApplicationCommandDelete(r.User.ID, "", cmd.ID)
	}

	commands := []*discordgo.ApplicationCommand{
		{
			Name:                     "setup",
			Description:              "Создать все каналы и категории сервера",
			DefaultMemberPermissions: ptrInt64(discordgo.PermissionAdministrator),
		},
		{
			Name:                     "delete",
			Description:              "Удалить все каналы и роли бота",
			DefaultMemberPermissions: ptrInt64(discordgo.PermissionAdministrator),
		},
		{
			Name:        "profile",
			Description: "Посмотреть профиль",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Пользователь",
					Required:    false,
				},
			},
		},
		{
			Name:        "nickname",
			Description: "Изменить серверный и игровой ник",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "nick",
					Description: "Новый игровой ник",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "game_id",
					Description: "Новый игровой ID (необязательно)",
					Required:    false,
				},
			},
		},
	}

	for _, cmd := range commands {
		_, err := s.ApplicationCommandCreate(r.User.ID, "", cmd)
		if err != nil {
			log.Printf("Error creating command %s: %v", cmd.Name, err)
		}
	}
}

func ptrInt64(v int64) *int64 { return &v }

func buildServerNick(id int, nick string) string {
	return fmt.Sprintf("#%d | %s", id, nick)
}

func isManagedChannelName(name string) bool {
	managedExact := map[string]bool{
		"REGISTRATION": true,
		"🪪│регистрация": true,
		"📍 | SEND RESULT'S": true,
		"📌│отправить-результаты": true,
		"📊│история-игр": true,
		"📁│приоритет-игр": true,
		"📁│обычный-рег": true,
		"ranked": true,
	}
	if managedExact[name] {
		return true
	}
	if strings.HasPrefix(name, "Lobby ") {
		return true
	}
	if strings.Contains(name, "PRO LEAGUE") || strings.Contains(name, "PRO REMASTERED") || strings.Contains(name, "DIVISION LEAGUE") || strings.Contains(name, "DIVISION REMASTERED") || strings.Contains(name, "PROSPECT LEAGUE") || strings.Contains(name, "PROSPECT REMASTERED") || strings.Contains(name, "DEFAULT LEAGUE") || strings.Contains(name, "DEFAULT REMASTERED") {
		return true
	}
	return false
}

func probeChannelCreateAvailability(s *discordgo.Session, guildID string) (time.Duration, error) {
	payload := map[string]any{"name": "SETUP-PROBE", "type": 4}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest("POST", "https://discord.com/api/v10/guilds/"+guildID+"/channels", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bot "+botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 429 {
		var rl struct {
			Message    string  `json:"message"`
			RetryAfter float64 `json:"retry_after"`
		}
		_ = json.Unmarshal(respBody, &rl)
		return time.Duration(rl.RetryAfter * float64(time.Second)), fmt.Errorf("rate_limited")
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("probe failed: %s %s", resp.Status, string(respBody))
	}

	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(respBody, &created)
	if created.ID != "" {
		_, _ = s.ChannelDelete(created.ID)
	}
	return 0, nil
}

func onGuildMemberAdd(s *discordgo.Session, m *discordgo.GuildMemberAdd) {
	_, err := getOrCreateUser(m.User.ID, m.User.Username)
	if err != nil {
		return
	}

	roles, _ := s.GuildRoles(m.GuildID)
	for _, r := range roles {
		if r.Name == "No registred" {
			_ = s.GuildMemberRoleAdd(m.GuildID, m.User.ID, r.ID)
			break
		}
	}
}

// ── Screenshot AI Recognition ───────────────────────────────────────────

type AIPlayerStat struct {
	Nickname string `json:"nickname"`
	Kills    int    `json:"kills"`
	Deaths   int    `json:"deaths"`
	Assists  int    `json:"assists"`
	Team     string `json:"team"` // "CT" or "T"
}

type AIMatchResult struct {
	IsValidScoreboard bool           `json:"is_valid_scoreboard"`
	Confidence        float64        `json:"confidence"`
	ScoreCT           int            `json:"score_ct"`
	ScoreT            int            `json:"score_t"`
	Map               string         `json:"map"`
	Players           []AIPlayerStat `json:"players"`
}

func recognizeScreenshot(imageURL string) (*AIMatchResult, error) {
	apiKey := os.Getenv("AI_API_KEY")
	baseURL := os.Getenv("AI_BASE_URL")
	if apiKey == "" || baseURL == "" {
		return nil, fmt.Errorf("AI_API_KEY or AI_BASE_URL not set")
	}

	prompt := `Analyze this image and determine if it is a REAL FINAL Standoff 2 scoreboard/table screenshot.
Return ONLY valid JSON with this exact structure:
{"is_valid_scoreboard": <true_or_false>, "confidence": <0.0_to_1.0>, "score_ct": <int>, "score_t": <int>, "map": "<map name>", "players": [{"nickname": "<name>", "kills": <int>, "deaths": <int>, "assists": <int>, "team": "<CT or T>"}]}
Rules:
- Set is_valid_scoreboard=true ONLY if this is clearly the final end-match tab/scoreboard from Standoff 2
- If it is any random photo, lobby screenshot, cropped nonsense, menu, profile, chat, or non-final image => is_valid_scoreboard=false
- Confidence must reflect certainty that this is a valid final scoreboard
- Include all 10 players (5 per team) if valid
- team is "CT" or "T"
- Read K/D/A carefully from the scoreboard columns
- Map name in English (Sandstone, Rust, Province, Breeze, Prison, Hanami, Dune)
- Return ONLY the JSON object, no markdown, no explanation`

	payload := map[string]any{
		"model": "gpt-4o-mini",
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": prompt},
					{"type": "image_url", "image_url": map[string]string{"url": imageURL}},
				},
			},
		},
		"max_tokens": 1000,
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("AI API error %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse AI response: %v", err)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("no AI response choices")
	}

	content := apiResp.Choices[0].Message.Content
	// Strip markdown code blocks if present
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var result AIMatchResult
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return nil, fmt.Errorf("parse AI JSON: %v (raw: %s)", err, content[:min(len(content), 200)])
	}
	if !result.IsValidScoreboard || result.Confidence < 0.82 {
		return nil, fmt.Errorf("это не финальный таб или уверенность слишком низкая (valid=%v, confidence=%.2f)", result.IsValidScoreboard, result.Confidence)
	}
	if len(result.Players) < 8 {
		return nil, fmt.Errorf("распознано слишком мало игроков (%d)", len(result.Players))
	}
	return &result, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func buildVerificationEmbed(matchID int, result *AIMatchResult, senderID string, imageURL string) *discordgo.MessageEmbed {
	teamA := ""
	teamB := ""
	for _, p := range result.Players {
		kd := fmt.Sprintf("%.2f", float64(p.Kills)/float64(max(p.Deaths, 1)))
		line := fmt.Sprintf("**%s** — %d/%d/%d (K/D %s)\n", p.Nickname, p.Kills, p.Deaths, p.Assists, kd)
		if p.Team == "CT" {
			teamA += line
		} else {
			teamB += line
		}
	}
	if teamA == "" { teamA = "—" }
	if teamB == "" { teamB = "—" }

	mapEmoji := "🗺️"
	for _, m := range Maps {
		if strings.EqualFold(m.Name, result.Map) {
			mapEmoji = m.Emoji
			break
		}
	}

	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("📋 Результат матча #%d — на проверку", matchID),
		Description: fmt.Sprintf("**Отправил:** <@%s>\n**Карта:** %s %s · формат: до 13\n\n📊 Распознано со скриншота: **%d : %d**", senderID, mapEmoji, result.Map, result.ScoreCT, result.ScoreT),
		Color:       0xf1c40f,
		Fields: []*discordgo.MessageEmbedField{
			{Name: fmt.Sprintf("🛡️ CT — %d · K/A/D", result.ScoreCT), Value: teamA, Inline: true},
			{Name: fmt.Sprintf("💣 T — %d · K/A/D", result.ScoreT), Value: teamB, Inline: true},
		},
		Image:  &discordgo.MessageEmbedImage{URL: imageURL},
		Footer: &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Матч #%d • %s", matchID, time.Now().Format("02.01.2006 15:04"))},
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot {
		return
	}
	// Только в ветках "Отчет матча #N"
	ch, err := s.Channel(m.ChannelID)
	if err != nil || ch == nil {
		return
	}
	if ch.Type != discordgo.ChannelTypeGuildPublicThread && ch.Type != discordgo.ChannelTypeGuildPrivateThread {
		return
	}
	if !strings.HasPrefix(ch.Name, "Отчет матча #") {
		return
	}

	// Извлечь номер матча из названия ветки
	matchNumStr := strings.TrimPrefix(ch.Name, "Отчет матча #")
	matchID, err := strconv.Atoi(matchNumStr)
	if err != nil {
		return
	}

	// Проверить что есть вложение-картинка
	var imageURL string
	for _, att := range m.Attachments {
		if strings.HasPrefix(att.ContentType, "image/") {
			imageURL = att.URL
			break
		}
	}
	if imageURL == "" {
		return
	}

	// Распознать скриншот через AI
	_, _ = s.ChannelMessageSend(m.ChannelID, "🔍 Распознаю скриншот через AI...")

	result, err := recognizeScreenshot(imageURL)
	if err != nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Ошибка распознавания: %v", err))
		log.Printf("[AI] Recognition error for match #%d: %v", matchID, err)
		return
	}

	_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("✅ Распознано! Счёт: **%d : %d** на карте **%s**. Отправляю на проверку...", result.ScoreCT, result.ScoreT, result.Map))

	// Сохранить результат в БД
	_, _ = execDB("UPDATE matches SET score_t = ?, score_ct = ?, map_name = ? WHERE id = ?", result.ScoreT, result.ScoreCT, result.Map, matchID)

	// Определить канал для проверки (приоритет или обычный)
	// Пока отправляем в обычный-рег. TODO: проверять наличие премиума у отправителя
	guildID := ch.GuildID
	channels, _ := s.GuildChannels(guildID)
	var targetChID string
	for _, c := range channels {
		if c.Name == "📁│обычный-рег" && c.Type == discordgo.ChannelTypeGuildText {
			targetChID = c.ID
			break
		}
	}
	if targetChID == "" {
		// Fallback: приоритет-игр
		for _, c := range channels {
			if c.Name == "📁│приоритет-игр" && c.Type == discordgo.ChannelTypeGuildText {
				targetChID = c.ID
				break
			}
		}
	}
	if targetChID == "" {
		_, _ = s.ChannelMessageSend(m.ChannelID, "❌ Не найден канал для проверки результатов.")
		return
	}

	// Построить и отправить карточку проверки
	embed := buildVerificationEmbed(matchID, result, m.Author.ID, imageURL)
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Подтвердить результат", Style: discordgo.SuccessButton, CustomID: fmt.Sprintf("confirm_result_%d_%s", matchID, m.ChannelID), Emoji: &discordgo.ComponentEmoji{Name: "✅"}},
			discordgo.Button{Label: "Счёт", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("edit_score_%d", matchID), Emoji: &discordgo.ComponentEmoji{Name: "📝"}},
			discordgo.Button{Label: "Статистика", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("edit_stats_%d", matchID), Emoji: &discordgo.ComponentEmoji{Name: "📊"}},
		}},
	}

	_, sendErr := s.ChannelMessageSendComplex(targetChID, &discordgo.MessageSend{
		Embeds:     []*discordgo.MessageEmbed{embed},
		Components: components,
	})
	if sendErr != nil {
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Ошибка отправки на проверку: %v", sendErr))
		return
	}

	_, _ = s.ChannelMessageSend(m.ChannelID, "📤 Карточка отправлена на проверку администраторам!")
}

// ── Voice State Update (lobby detection) ────────────────────────────────

func onVoiceStateUpdate(s *discordgo.Session, v *discordgo.VoiceStateUpdate) {
	// Если пользователь покинул T/CT войс — проверим автоудаление через 3 минуты
	if v.BeforeUpdate != nil && v.BeforeUpdate.ChannelID != "" {
		oldCh, _ := s.Channel(v.BeforeUpdate.ChannelID)
		if oldCh != nil && (strings.HasPrefix(oldCh.Name, "T │ Матч #") || strings.HasPrefix(oldCh.Name, "CT │ Матч #")) {
			matchNum := 0
			fmt.Sscanf(oldCh.Name, "T │ Матч #%d", &matchNum)
			if matchNum == 0 {
				fmt.Sscanf(oldCh.Name, "CT │ Матч #%d", &matchNum)
			}
			if matchNum > 0 {
				state := &PickBanState{MatchID: matchNum, GuildID: v.GuildID}
				state.TVoiceID = oldCh.ID
				channels, _ := s.GuildChannels(v.GuildID)
				for _, c := range channels {
					if c.Type == discordgo.ChannelTypeGuildVoice && strings.Contains(c.Name, fmt.Sprintf("Матч #%d", matchNum)) {
						if strings.HasPrefix(c.Name, "T │") { state.TVoiceID = c.ID }
						if strings.HasPrefix(c.Name, "CT │") { state.CTVoiceID = c.ID }
					}
				}
				scheduleTeamVoicesCleanup(s, v.GuildID, state)
			}
		}
	}

	if v.ChannelID == "" {
		return
	}
	if v.BeforeUpdate != nil && v.BeforeUpdate.ChannelID == v.ChannelID {
		return
	}

	ch, err := s.Channel(v.ChannelID)
	if err != nil || ch.Type != discordgo.ChannelTypeGuildVoice {
		return
	}
	if !strings.HasPrefix(ch.Name, "Lobby") {
		return
	}

	// Ensure user is in DB
	member, err := s.GuildMember(v.GuildID, v.UserID)
	if err != nil {
		return
	}
	userID, _ := getOrCreateUser(v.UserID, member.User.Username)

	// Set nickname to #ID | registered game nick
	displayNick := member.User.Username
	_ = queryRowDB("SELECT COALESCE(NULLIF(game_nick, ''), username) FROM users WHERE discord_id = ?", v.UserID).Scan(&displayNick)
	nick := buildServerNick(userID, displayNick)
	guild, _ := s.Guild(v.GuildID)
	if guild != nil && guild.OwnerID != v.UserID {
		s.GuildMemberNickname(v.GuildID, v.UserID, nick)
	}

	// Count members in voice channel
	g, err := s.State.Guild(v.GuildID)
	if err != nil {
		return
	}
	count := 0
	for _, vs := range g.VoiceStates {
		if vs.ChannelID == v.ChannelID {
			count++
		}
	}

	minPlayers := 10
	if mp := os.Getenv("MIN_LOBBY_PLAYERS"); mp != "" {
		if n, err := strconv.Atoi(mp); err == nil {
			minPlayers = n
		}
	}

	if count >= minPlayers {
		// Find the #ranked channel in same category
		if ch.ParentID == "" {
			return
		}

		channels, _ := s.GuildChannels(v.GuildID)
		var rankedCh *discordgo.Channel
		for _, c := range channels {
			if c.ParentID == ch.ParentID && c.Name == "ranked" && c.Type == discordgo.ChannelTypeGuildText {
				rankedCh = c
				break
			}
		}
		if rankedCh == nil {
			return
		}

		// Collect players from voice
		var players []string
		for _, vs := range g.VoiceStates {
			if vs.ChannelID == v.ChannelID {
				players = append(players, vs.UserID)
			}
		}

		// Get category name for league info
		parentCh, _ := s.Channel(ch.ParentID)
		leagueName := ""
		if parentCh != nil {
			leagueName = parentCh.Name
		}

		startPickBan(s, v.GuildID, rankedCh.ID, players, leagueName)
	}
}

// ── Start Pick/Ban ──────────────────────────────────────────────────────

func startPickBan(s *discordgo.Session, guildID, channelID string, playerIDs []string, league string) {
	pickBanMu.Lock()
	if _, exists := activePickBans[channelID]; exists {
		pickBanMu.Unlock()
		return
	}
	pickBanMu.Unlock()

	// Create match in DB
	matchID := 0
	if dbDialect == "postgres" {
		if err := queryRowDB("INSERT INTO matches (status, category) VALUES ('picking', ?) RETURNING id", league).Scan(&matchID); err != nil {
			log.Println("Error creating match:", err)
			return
		}
	} else {
		res, err := execDB("INSERT INTO matches (status, category) VALUES ('picking', ?)", league)
		if err != nil {
			log.Println("Error creating match:", err)
			return
		}
		matchID64, _ := res.LastInsertId()
		matchID = int(matchID64)
	}

	// Build player info
	var allPlayers []PlayerInfo
	for _, pid := range playerIDs {
		member, err := s.GuildMember(guildID, pid)
		if err != nil {
			continue
		}
		uid, _ := getOrCreateUser(pid, member.User.Username)
		allPlayers = append(allPlayers, PlayerInfo{
			DiscordID: pid,
			Username:  member.User.Username,
			UniqueID:  uid,
		})
	}

	// Shuffle and split
	rand.Shuffle(len(allPlayers), func(i, j int) { allPlayers[i], allPlayers[j] = allPlayers[j], allPlayers[i] })

	half := len(allPlayers) / 2
	tTeam := allPlayers[:half]
	ctTeam := allPlayers[half:]

	// Random captains
	tCapIdx := rand.Intn(len(tTeam))
	ctCapIdx := rand.Intn(len(ctTeam))
	tTeam[tCapIdx].IsCaptain = true
	ctTeam[ctCapIdx].IsCaptain = true

	// Save to DB
	for _, p := range tTeam {
		isCap := 0
		if p.IsCaptain {
			isCap = 1
		}
		_, _ = execDB("INSERT INTO match_players (match_id, user_id, team, is_captain) VALUES (?, ?, 'T', ?)", matchID, p.UniqueID, isCap)
	}
	for _, p := range ctTeam {
		isCap := 0
		if p.IsCaptain {
			isCap = 1
		}
		_, _ = execDB("INSERT INTO match_players (match_id, user_id, team, is_captain) VALUES (?, ?, 'CT', ?)", matchID, p.UniqueID, isCap)
	}

	state := &PickBanState{
		MatchID:    matchID,
		GuildID:    guildID,
		ChannelID:  channelID,
		TCapID:     tTeam[tCapIdx].DiscordID,
		CTCapID:    ctTeam[ctCapIdx].DiscordID,
		TTeam:      tTeam,
		CTTeam:     ctTeam,
		BannedMaps: []string{},
		Turn:       "T",
		League:     league,
		BanVersion: 1,
	}
	pickBanMu.Lock()
	activePickBans[channelID] = state
	pickBanMu.Unlock()

	// Mention all players
	mentions := ""
	for _, p := range append(tTeam, ctTeam...) {
		mentions += fmt.Sprintf("<@%s> ", p.DiscordID)
	}

	embed := buildPickBanEmbed(state)

	options := buildMapSelectOptions(state)
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    fmt.Sprintf("ban_%d", matchID),
					Placeholder: "T: забань карту",
					Options:     options,
				},
			},
		},
	}

	msg, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:    mentions,
		Embeds:     []*discordgo.MessageEmbed{embed},
		Components: components,
	})
	if err != nil {
		log.Println("Error sending pick/ban:", err)
		return
	}
	state.MessageID = msg.ID
	scheduleAutoBan(s, state)
}

func buildPickBanEmbed(state *PickBanState) *discordgo.MessageEmbed {
	remaining := getRemainingMaps(state.BannedMaps)

	currentCapID := state.TCapID
	currentCapName := ""
	turnEmoji := "💣"
	if state.Turn == "CT" {
		currentCapID = state.CTCapID
		turnEmoji = "🛡️"
	}
	// Find captain name
	for _, p := range append(state.TTeam, state.CTTeam...) {
		if p.DiscordID == currentCapID {
			currentCapName = p.Username
			break
		}
	}

	// Map list with strikethrough for banned
	mapList := ""
	for _, m := range Maps {
		banned := false
		for _, b := range state.BannedMaps {
			if b == m.Name {
				banned = true
				break
			}
		}
		if banned {
			mapList += fmt.Sprintf("> ~~%s %s~~\n", m.Emoji, m.Name)
		} else {
			mapList += fmt.Sprintf("> %s %s\n", m.Emoji, m.Name)
		}
	}

	// T team list
	tList := ""
	for _, p := range state.TTeam {
		prefix := "•"
		crown := ""
		if p.IsCaptain {
			crown = " 👑"
		}
		tList += fmt.Sprintf("%s%s @#%d | %s 🔗\n", prefix, crown, p.UniqueID, p.Username)
	}

	// CT team list
	ctList := ""
	for _, p := range state.CTTeam {
		prefix := "•"
		crown := ""
		if p.IsCaptain {
			crown = " 👑"
		}
		ctList += fmt.Sprintf("%s%s @#%d | %s 🔗\n", prefix, crown, p.UniqueID, p.Username)
	}

	capMention := fmt.Sprintf("<@%s>", currentCapID)

	return &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("🗺️ Матч #%d · бан карт", state.MatchID),
		Description: fmt.Sprintf("**Банит** %s **%s** — %s | %s\n**Осталось карт:** %d\n\n%s\n*Составы собраны по среднему фрагу · 🔗 — пати играет вместе*", turnEmoji, state.Turn, capMention, currentCapName, len(remaining), mapList),
		Color:       0xe91e63,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "💣 T",
				Value:  tList,
				Inline: true,
			},
			{
				Name:   "🛡️ CT",
				Value:  ctList,
				Inline: true,
			},
		},
	}
}

func getRemainingMaps(banned []string) []MapInfo {
	var remaining []MapInfo
	for _, m := range Maps {
		isBanned := false
		for _, b := range banned {
			if b == m.Name {
				isBanned = true
				break
			}
		}
		if !isBanned {
			remaining = append(remaining, m)
		}
	}
	return remaining
}

func buildMapSelectOptions(state *PickBanState) []discordgo.SelectMenuOption {
	remaining := getRemainingMaps(state.BannedMaps)
	var options []discordgo.SelectMenuOption
	for _, m := range remaining {
		options = append(options, discordgo.SelectMenuOption{
			Label: m.Name,
			Value: m.Name,
			Emoji: &discordgo.ComponentEmoji{Name: m.Emoji},
		})
	}
	return options
}

func updatePickBanMessage(s *discordgo.Session, state *PickBanState) {
	embed := buildPickBanEmbed(state)
	placeholder := fmt.Sprintf("%s: забань карту", state.Turn)
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.SelectMenu{CustomID: fmt.Sprintf("ban_%d", state.MatchID), Placeholder: placeholder, Options: buildMapSelectOptions(state)},
		}},
	}
	_, _ = s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: state.MessageID, Channel: state.ChannelID, Embeds: &[]*discordgo.MessageEmbed{embed}, Components: &components})
}

func scheduleAutoBan(s *discordgo.Session, state *PickBanState) {
	version := state.BanVersion
	channelID := state.ChannelID
	go func() {
		time.Sleep(15 * time.Second)
		pickBanMu.Lock()
		current, ok := activePickBans[channelID]
		if !ok || current.BanVersion != version {
			pickBanMu.Unlock()
			return
		}
		remaining := getRemainingMaps(current.BannedMaps)
		if len(remaining) <= 1 {
			pickBanMu.Unlock()
			return
		}
		autoMap := remaining[rand.Intn(len(remaining))].Name
		current.BannedMaps = append(current.BannedMaps, autoMap)
		if current.Turn == "T" {
			current.Turn = "CT"
		} else {
			current.Turn = "T"
		}
		current.BanVersion++
		remainingAfter := getRemainingMaps(current.BannedMaps)
		pickBanMu.Unlock()

		_, _ = s.ChannelMessageSend(current.ChannelID, fmt.Sprintf("⏱️ Капитан не забанил карту за 15 секунд — карта **%s** забанена автоматически.", autoMap))

		if len(remainingAfter) == 1 {
			finalizeMatchStart(s, current, remainingAfter[0])
			return
		}
		updatePickBanMessage(s, current)
		scheduleAutoBan(s, current)
	}()
}

func scheduleTeamVoicesCleanup(s *discordgo.Session, guildID string, state *PickBanState) {
	matchID := state.MatchID
	go func() {
		time.Sleep(3 * time.Minute)
		g, err := s.State.Guild(guildID)
		if err != nil {
			g, err = s.Guild(guildID)
			if err != nil || g == nil {
				return
			}
		}
		countT, countCT := 0, 0
		for _, vs := range g.VoiceStates {
			if state.TVoiceID != "" && vs.ChannelID == state.TVoiceID { countT++ }
			if state.CTVoiceID != "" && vs.ChannelID == state.CTVoiceID { countCT++ }
		}
		if countT == 0 && countCT == 0 {
			if state.TVoiceID != "" { _, _ = s.ChannelDelete(state.TVoiceID) }
			if state.CTVoiceID != "" { _, _ = s.ChannelDelete(state.CTVoiceID) }
		}
		_ = matchID
	}()
}

func finalizeMatchStart(s *discordgo.Session, state *PickBanState, finalMap MapInfo) {
	_, _ = execDB("UPDATE matches SET map_name = ?, status = 'playing' WHERE id = ?", finalMap.Name, state.MatchID)
	pickBanMu.Lock()
	delete(activePickBans, state.ChannelID)
	pickBanMu.Unlock()

	_, _ = s.ChannelMessageEditComplex(&discordgo.MessageEdit{ID: state.MessageID, Channel: state.ChannelID, Embeds: &[]*discordgo.MessageEmbed{}, Components: &[]discordgo.MessageComponent{}})

	hostMention := fmt.Sprintf("<@%s>", state.TCapID)
	mentions := ""
	for _, p := range append(state.TTeam, state.CTTeam...) { mentions += fmt.Sprintf("<@%s> ", p.DiscordID) }

	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("#%d · %s %s", state.MatchID, finalMap.Emoji, finalMap.Name),
		Description: "Игра идёт до **13 раундов**\n**Комнаты:** 🔒 Нет доступа · 🔒 Нет доступа — зайти может только свой состав\n*Ждём ссылку на лобби от хоста...*",
		Color:       0xe91e63,
		Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("ID матча для отправки результата: %d • %s", state.MatchID, time.Now().Format("02.01.2006 15:04"))},
	}

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Отправить ссылку на лобби", Style: discordgo.SuccessButton, CustomID: fmt.Sprintf("lobby_link_%d", state.MatchID), Emoji: &discordgo.ComponentEmoji{Name: "🔗"}},
			discordgo.Button{Label: "Получить ID хоста", Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("hostid_%d_%s", state.MatchID, state.TCapID), Emoji: &discordgo.ComponentEmoji{Name: "🆔"}},
			discordgo.Button{Label: "Игрок не зашёл (0/6)", Style: discordgo.DangerButton, CustomID: fmt.Sprintf("noshow_%d", state.MatchID), Emoji: &discordgo.ComponentEmoji{Name: "🚫"}},
		}},
	}
	_, _ = s.ChannelMessageSendComplex(state.ChannelID, &discordgo.MessageSend{Content: mentions, Embeds: []*discordgo.MessageEmbed{embed}, Components: components})
	_, _ = s.ChannelMessageSend(state.ChannelID, fmt.Sprintf("👑 %s — ты хост матча #%d. Создай лобби и нажми **Отправить ссылку на лобби**.", hostMention, state.MatchID))

	// Создание приватных T/CT войсов и автоперенос
	ch, _ := s.Channel(state.ChannelID)
	if ch != nil {
		denyAll := int64(discordgo.PermissionViewChannel)
		allowVoice := int64(discordgo.PermissionViewChannel | discordgo.PermissionVoiceConnect | discordgo.PermissionVoiceSpeak | discordgo.PermissionVoiceUseVAD)
		adminRoleIDs := []string{}
		allRoles, _ := s.GuildRoles(state.GuildID)
		for _, r := range allRoles {
			if r.Name == "Game Admin" || r.Name == "Ticket Admin" { adminRoleIDs = append(adminRoleIDs, r.ID) }
		}
		tOverwrites := []*discordgo.PermissionOverwrite{{ID: state.GuildID, Type: discordgo.PermissionOverwriteTypeRole, Deny: denyAll}}
		for _, p := range state.TTeam { tOverwrites = append(tOverwrites, &discordgo.PermissionOverwrite{ID: p.DiscordID, Type: discordgo.PermissionOverwriteTypeMember, Allow: allowVoice}) }
		for _, rid := range adminRoleIDs { tOverwrites = append(tOverwrites, &discordgo.PermissionOverwrite{ID: rid, Type: discordgo.PermissionOverwriteTypeRole, Allow: allowVoice}) }
		ctOverwrites := []*discordgo.PermissionOverwrite{{ID: state.GuildID, Type: discordgo.PermissionOverwriteTypeRole, Deny: denyAll}}
		for _, p := range state.CTTeam { ctOverwrites = append(ctOverwrites, &discordgo.PermissionOverwrite{ID: p.DiscordID, Type: discordgo.PermissionOverwriteTypeMember, Allow: allowVoice}) }
		for _, rid := range adminRoleIDs { ctOverwrites = append(ctOverwrites, &discordgo.PermissionOverwrite{ID: rid, Type: discordgo.PermissionOverwriteTypeRole, Allow: allowVoice}) }
		parentID := ch.ParentID
		tVoice, _ := s.GuildChannelCreateComplex(state.GuildID, discordgo.GuildChannelCreateData{Name: fmt.Sprintf("T │ Матч #%d", state.MatchID), Type: discordgo.ChannelTypeGuildVoice, ParentID: parentID, UserLimit: 5, PermissionOverwrites: tOverwrites})
		ctVoice, _ := s.GuildChannelCreateComplex(state.GuildID, discordgo.GuildChannelCreateData{Name: fmt.Sprintf("CT │ Матч #%d", state.MatchID), Type: discordgo.ChannelTypeGuildVoice, ParentID: parentID, UserLimit: 5, PermissionOverwrites: ctOverwrites})
		if tVoice != nil { state.TVoiceID = tVoice.ID }
		if ctVoice != nil { state.CTVoiceID = ctVoice.ID }
		state.VoiceParentID = parentID
		time.Sleep(1 * time.Second)
		if tVoice != nil { for _, p := range state.TTeam { _ = s.GuildMemberMove(state.GuildID, p.DiscordID, &tVoice.ID) } }
		if ctVoice != nil { for _, p := range state.CTTeam { _ = s.GuildMemberMove(state.GuildID, p.DiscordID, &ctVoice.ID) } }
		scheduleTeamVoicesCleanup(s, state.GuildID, state)
	}
}

// ── Interaction Handler ─────────────────────────────────────────────────

func onInteraction(s *discordgo.Session, i *discordgo.InteractionCreate) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("PANIC in interaction handler: %v", r)
			_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Внутренняя ошибка бота. Попробуй ещё раз.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
		}
	}()

	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		handleCommand(s, i)
	case discordgo.InteractionMessageComponent:
		handleComponent(s, i)
	case discordgo.InteractionModalSubmit:
		handleModal(s, i)
	}
}

func handleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.ApplicationCommandData().Name {
	case "setup":
		handleSetup(s, i)
	case "delete":
		handleDelete(s, i)
	case "profile":
		handleProfile(s, i)
	case "nickname":
		handleNickname(s, i)
	}
}

// ── /setup ──────────────────────────────────────────────────────────────

func handleSetup(s *discordgo.Session, i *discordgo.InteractionCreate) {
	setupMu.Lock()
	if setupInProgress[i.GuildID] {
		setupMu.Unlock()
		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: "⏳ Настройка уже идёт.", Flags: discordgo.MessageFlagsEphemeral},
		})
		return
	}
	setupInProgress[i.GuildID] = true
	setupMu.Unlock()

	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: "⏳ Проверяю и обновляю структуру сервера...", Flags: discordgo.MessageFlagsEphemeral},
	})

	guildID := i.GuildID
	cmdCh := i.ChannelID
	minPlayers := 10
	if mp := os.Getenv("MIN_LOBBY_PLAYERS"); mp != "" {
		if n, err := strconv.Atoi(mp); err == nil && n > 0 {
			minPlayers = n
		}
	}

	go func() {
		defer func() {
			setupMu.Lock(); delete(setupInProgress, guildID); setupMu.Unlock()
			if r := recover(); r != nil { log.Printf("PANIC setup: %v", r) }
		}()

		existingChs, err := s.GuildChannels(guildID)
		if err != nil { _, _ = s.ChannelMessageSend(cmdCh, "Не удалось получить каналы."); return }
		existingRoles, _ := s.GuildRoles(guildID)
		roleByName := map[string]*discordgo.Role{}
		for _, r := range existingRoles { roleByName[r.Name] = r }

		ensureRole := func(name string) string {
			if r, ok := roleByName[name]; ok { return r.ID }
			r, err := s.GuildRoleCreate(guildID, &discordgo.RoleParams{Name: name})
			if err != nil || r == nil { return "" }
			roleByName[name] = r
			return r.ID
		}
		noRegRoleID     := ensureRole("No registred")
		gameAdminRoleID := ensureRole("Game Admin")
		tickAdminRoleID := ensureRole("Ticket Admin")
		leagueRoleIDs   := map[string]string{}
		for _, lr := range LeagueRoles { leagueRoleIDs[lr] = ensureRole(lr) }

		denyAll    := int64(discordgo.PermissionViewChannel)
		viewOnly   := int64(discordgo.PermissionViewChannel | discordgo.PermissionReadMessageHistory)
		fullAccess := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages |
			discordgo.PermissionReadMessageHistory | discordgo.PermissionVoiceConnect |
			discordgo.PermissionVoiceSpeak | discordgo.PermissionVoiceUseVAD)
		pubWrite   := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionReadMessageHistory)

		applyLeagueCatPerms := func(catID, ownerRole string) {
			_ = s.ChannelPermissionSet(catID, guildID, discordgo.PermissionOverwriteTypeRole, 0, denyAll)
			if noRegRoleID != "" { _ = s.ChannelPermissionSet(catID, noRegRoleID, discordgo.PermissionOverwriteTypeRole, 0, denyAll) }
			for _, rid := range leagueRoleIDs { _ = s.ChannelPermissionSet(catID, rid, discordgo.PermissionOverwriteTypeRole, viewOnly, 0) }
			if rid := leagueRoleIDs[ownerRole]; rid != "" { _ = s.ChannelPermissionSet(catID, rid, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
			if gameAdminRoleID != "" { _ = s.ChannelPermissionSet(catID, gameAdminRoleID, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
			if tickAdminRoleID != "" { _ = s.ChannelPermissionSet(catID, tickAdminRoleID, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
		}

		applyRankedPerms := func(id string) {
			_ = s.ChannelPermissionSet(id, guildID, discordgo.PermissionOverwriteTypeRole,
				int64(discordgo.PermissionViewChannel|discordgo.PermissionReadMessageHistory),
				int64(discordgo.PermissionSendMessages))
			if session.State != nil && session.State.User != nil {
				_ = s.ChannelPermissionSet(id, session.State.User.ID, discordgo.PermissionOverwriteTypeMember,
					int64(discordgo.PermissionViewChannel|discordgo.PermissionSendMessages|discordgo.PermissionReadMessageHistory|discordgo.PermissionEmbedLinks), 0)
			}
		}

		ensureCat := func(name string) (*discordgo.Channel, bool) {
			for _, ch := range existingChs { if ch.Name == name && ch.Type == discordgo.ChannelTypeGuildCategory { return ch, false } }
			cat, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{Name: name, Type: discordgo.ChannelTypeGuildCategory})
			if err != nil { return nil, false }
			time.Sleep(300 * time.Millisecond)
			return cat, true
		}
		ensureText := func(name, parentID string) (*discordgo.Channel, bool) {
			for _, ch := range existingChs { if ch.Name == name && ch.Type == discordgo.ChannelTypeGuildText && ch.ParentID == parentID { return ch, false } }
			ch, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{Name: name, Type: discordgo.ChannelTypeGuildText, ParentID: parentID})
			if err != nil { return nil, false }
			return ch, true
		}
		ensureVoice := func(name, parentID string, limit int) {
			for _, ch := range existingChs { if ch.Name == name && ch.Type == discordgo.ChannelTypeGuildVoice && ch.ParentID == parentID { return } }
			_, _ = s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{Name: name, Type: discordgo.ChannelTypeGuildVoice, ParentID: parentID, UserLimit: limit})
		}

		var reorderList []*discordgo.Channel
		pos := 0

		// REGISTRATION
		regCat, _ := ensureCat("REGISTRATION")
		if regCat != nil {
			reorderList = append(reorderList, &discordgo.Channel{ID: regCat.ID, Position: pos}); pos++
			_ = s.ChannelPermissionSet(regCat.ID, guildID, discordgo.PermissionOverwriteTypeRole, pubWrite, 0)
			if noRegRoleID != "" { _ = s.ChannelPermissionSet(regCat.ID, noRegRoleID, discordgo.PermissionOverwriteTypeRole, pubWrite, 0) }
			for _, rid := range leagueRoleIDs { _ = s.ChannelPermissionSet(regCat.ID, rid, discordgo.PermissionOverwriteTypeRole, 0, denyAll) }
			if gameAdminRoleID != "" { _ = s.ChannelPermissionSet(regCat.ID, gameAdminRoleID, discordgo.PermissionOverwriteTypeRole, 0, denyAll) }
			if tickAdminRoleID != "" { _ = s.ChannelPermissionSet(regCat.ID, tickAdminRoleID, discordgo.PermissionOverwriteTypeRole, 0, denyAll) }
			regCh, created := ensureText("🪪│регистрация", regCat.ID)
			if regCh != nil && created {
				_, _ = s.ChannelMessageSendComplex(regCh.ID, &discordgo.MessageSend{
					Embeds: []*discordgo.MessageEmbed{{Title: "🪪 РЕГИСТРАЦИЯ", Color: 0x5865f2,
						Description: "Добро пожаловать!\n\n› Игровой ник (ник на сервере)\n› Игровой ID (для хоста лобби)\n\nЕсли уже зарегистрирован — **Войти**.",
					}},
					Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
						discordgo.Button{Label: "Регистрация", Style: discordgo.SuccessButton, CustomID: "open_register", Emoji: &discordgo.ComponentEmoji{Name: "🪪"}},
						discordgo.Button{Label: "Войти", Style: discordgo.PrimaryButton, CustomID: "login_existing", Emoji: &discordgo.ComponentEmoji{Name: "🔑"}},
					}}},
				})
			}
		}

		// Лиги
		for _, league := range LeagueConfig {
			for _, lobbyNum := range league.Lobbies {
				catName := fmt.Sprintf("%s | %s", league.Emoji, league.Name)
				cat, created := ensureCat(catName)
				if cat == nil { continue }
				reorderList = append(reorderList, &discordgo.Channel{ID: cat.ID, Position: pos}); pos++
				applyLeagueCatPerms(cat.ID, league.RoleName)
				rankedCh, _ := ensureText("ranked", cat.ID)
				if rankedCh != nil { applyRankedPerms(rankedCh.ID) }
				ensureVoice(fmt.Sprintf("Lobby %d", lobbyNum), cat.ID, minPlayers)
				status := "✅"
				if created { status = "🆕"; time.Sleep(2*time.Second) } else { time.Sleep(200*time.Millisecond) }
				_, _ = s.ChannelMessageSend(cmdCh, fmt.Sprintf("%s %s / Lobby %d", status, league.Name, lobbyNum))
			}
		}

		// SEND RESULT'S
		resCat, _ := ensureCat("📍 | SEND RESULT'S")
		if resCat != nil {
			reorderList = append(reorderList, &discordgo.Channel{ID: resCat.ID, Position: pos}); pos++
			_ = s.ChannelPermissionSet(resCat.ID, guildID, discordgo.PermissionOverwriteTypeRole, 0, denyAll)
			if noRegRoleID != "" { _ = s.ChannelPermissionSet(resCat.ID, noRegRoleID, discordgo.PermissionOverwriteTypeRole, 0, denyAll) }
			for _, rid := range leagueRoleIDs { _ = s.ChannelPermissionSet(resCat.ID, rid, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
			if gameAdminRoleID != "" { _ = s.ChannelPermissionSet(resCat.ID, gameAdminRoleID, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
			if tickAdminRoleID != "" { _ = s.ChannelPermissionSet(resCat.ID, tickAdminRoleID, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
			sendCh, created := ensureText("📌│отправить-результаты", resCat.ID)
			if sendCh != nil && created {
				_, _ = s.ChannelMessageSendComplex(sendCh.ID, &discordgo.MessageSend{
					Embeds: []*discordgo.MessageEmbed{{Title: "📌 ОТПРАВКА РЕЗУЛЬТАТА", Color: 0xe91e63,
						Description: "Сыграли матч? Отправьте результат.\n\n› Нажми **Отправить результат**\n› Укажи ID матча\n› Бот создаст ветку — скинь туда скриншот",
						Footer: &discordgo.MessageEmbedFooter{Text: "ELITE FPL • результаты"},
					}},
					Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
						discordgo.Button{Label: "Отправить результат", Style: discordgo.DangerButton, CustomID: "send_result", Emoji: &discordgo.ComponentEmoji{Name: "📝"}},
					}}},
				})
			}
			ensureText("📊│история-игр", resCat.ID)
			// приоритет-игр и обычный-рег — ТОЛЬКО для Game Admin / Ticket Admin
			for _, chName := range []string{"📁│приоритет-игр", "📁│обычный-рег"} {
				adminCh, _ := ensureText(chName, resCat.ID)
				if adminCh != nil {
					_ = s.ChannelPermissionSet(adminCh.ID, guildID, discordgo.PermissionOverwriteTypeRole, 0, denyAll)
					if noRegRoleID != "" { _ = s.ChannelPermissionSet(adminCh.ID, noRegRoleID, discordgo.PermissionOverwriteTypeRole, 0, denyAll) }
					for _, rid := range leagueRoleIDs { _ = s.ChannelPermissionSet(adminCh.ID, rid, discordgo.PermissionOverwriteTypeRole, 0, denyAll) }
					if gameAdminRoleID != "" { _ = s.ChannelPermissionSet(adminCh.ID, gameAdminRoleID, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
					if tickAdminRoleID != "" { _ = s.ChannelPermissionSet(adminCh.ID, tickAdminRoleID, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
				}
			}
		}

		time.Sleep(1 * time.Second)
		if len(reorderList) > 0 {
			_ = s.GuildChannelsReorder(guildID, reorderList)
		}

		defaultRoleID := leagueRoleIDs["Default League"]
		members, _ := s.GuildMembers(guildID, "", 1000)
		for _, m := range members {
			if m.User == nil || m.User.Bot { continue }
			uid, _ := getOrCreateUser(m.User.ID, m.User.Username)
			var isReg int
			_ = queryRowDB("SELECT is_registered FROM users WHERE discord_id = ?", m.User.ID).Scan(&isReg)
			if isReg == 1 {
				if defaultRoleID != "" { _ = s.GuildMemberRoleAdd(guildID, m.User.ID, defaultRoleID) }
				var nick string
				_ = queryRowDB("SELECT COALESCE(NULLIF(game_nick,''),username) FROM users WHERE discord_id=?", m.User.ID).Scan(&nick)
				g, _ := s.Guild(guildID)
				if g != nil && g.OwnerID != m.User.ID { _ = s.GuildMemberNickname(guildID, m.User.ID, buildServerNick(uid, nick)) }
			} else {
				if noRegRoleID != "" { _ = s.GuildMemberRoleAdd(guildID, m.User.ID, noRegRoleID) }
			}
		}
		_, _ = s.ChannelMessageSend(cmdCh, "✅ Готово! Сервер проверен и обновлён.")
		log.Printf("[SETUP] Done for guild %s", guildID)
	}()
}

func strPtr(s string) *string { return &s }

// ── /delete ─────────────────────────────────────────────────────────────

func handleDelete(s *discordgo.Session, i *discordgo.InteractionCreate) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: "🗑️ Удаляю все каналы..."},
	})

	channels, _ := s.GuildChannels(i.GuildID)
	for _, ch := range channels {
		s.ChannelDelete(ch.ID)
	}

	roles, _ := s.GuildRoles(i.GuildID)
	for _, r := range roles {
		if r.Name == "No registred" || r.Name == "Registered" || r.Name == "Game Admin" || r.Name == "Ticket Admin" {
			s.GuildRoleDelete(i.GuildID, r.ID)
		}
	}
}

// ── /nickname ───────────────────────────────────────────────────────────

func handleNickname(s *discordgo.Session, i *discordgo.InteractionCreate) {
	userID := i.Member.User.ID
	options := i.ApplicationCommandData().Options

	nick := ""
	gameID := ""
	for _, opt := range options {
		if opt.Name == "nick" {
			nick = opt.StringValue()
		}
		if opt.Name == "game_id" {
			gameID = opt.StringValue()
		}
	}

	uid, _ := getOrCreateUser(userID, i.Member.User.Username)
	if gameID != "" {
		_, _ = execDB("UPDATE users SET username = ?, game_nick = ?, game_id = ? WHERE discord_id = ?", nick, nick, gameID, userID)
	} else {
		_, _ = execDB("UPDATE users SET username = ?, game_nick = ? WHERE discord_id = ?", nick, nick, userID)
	}

	guild, _ := s.Guild(i.GuildID)
	if guild != nil && guild.OwnerID != userID {
		_ = s.GuildMemberNickname(i.GuildID, userID, buildServerNick(uid, nick))
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: fmt.Sprintf("✅ Ник обновлён: **%s**", buildServerNick(uid, nick)),
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// ── /profile ────────────────────────────────────────────────────────────

func handleProfile(s *discordgo.Session, i *discordgo.InteractionCreate) {
	targetUser := i.Member.User
	options := i.ApplicationCommandData().Options
	for _, opt := range options {
		if opt.Name == "user" {
			targetUser = opt.UserValue(s)
		}
	}

	var id int
	var username string
	var elo, games, wins, losses, kills, deaths, assists, mvps int
	var joinedAt string

	err := queryRowDB(`SELECT id, username, elo, games, wins, losses, kills, deaths, assists, mvps, joined_at 
		FROM users WHERE discord_id = ?`, targetUser.ID).Scan(&id, &username, &elo, &games, &wins, &losses, &kills, &deaths, &assists, &mvps, &joinedAt)

	if err != nil {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: "❌ Пользователь не найден. Используйте /register",
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	kd := 0.0
	if deaths > 0 {
		kd = float64(kills) / float64(deaths)
	}
	avgKills := 0.0
	if games > 0 {
		avgKills = float64(kills) / float64(games)
	}

	// Map stats
	mapStatsText := ""
	rows, err := queryDB("SELECT map_name, wins, losses, kills, deaths FROM map_stats WHERE user_id = ?", id)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var mn string
			var mw, ml, mk, md int
			rows.Scan(&mn, &mw, &ml, &mk, &md)
			mkd := 0.0
			if md > 0 {
				mkd = float64(mk) / float64(md)
			}
			wr := 0
			if mw+ml > 0 {
				wr = mw * 100 / (mw + ml)
			}
			mapStatsText += fmt.Sprintf("**%s** W=%d L=%d K/D=%.2f W/R=%d%%\n", mn, mw, ml, mkd, wr)
		}
	}
	if mapStatsText == "" {
		mapStatsText = "Нет данных"
	}

	embed := &discordgo.MessageEmbed{
		Title: fmt.Sprintf("%s  #%d", username, id),
		Description: fmt.Sprintf("ID: %s", targetUser.ID),
		Color:       0x1db954,
		Thumbnail:   &discordgo.MessageEmbedThumbnail{URL: targetUser.AvatarURL("256")},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "📊 Statistic", Value: fmt.Sprintf("**ELO:** %d\n**K/D:** %.2f\n**AVG:** %.1f", elo, kd, avgKills), Inline: true},
			{Name: "📅 Info", Value: fmt.Sprintf("**Games:** %d\n**MVP:** %d\n**Join Date:** %s", games, mvps, joinedAt[:10]), Inline: true},
			{Name: "⚔️ Stats", Value: fmt.Sprintf("**Убийства:** %d\n**Смерти:** %d\n**Ассисты:** %d\n**Победы:** %d\n**Поражения:** %d", kills, deaths, assists, wins, losses)},
			{Name: "🗺️ Map Statistic", Value: mapStatsText},
		},
	}

	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds: []*discordgo.MessageEmbed{embed},
		},
	})
}

// ── Component (buttons, selects) ────────────────────────────────────────

func handleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID

	// Confirm match result
	if strings.HasPrefix(customID, "confirm_result_") {
		rest := strings.TrimPrefix(customID, "confirm_result_")
		parts := strings.SplitN(rest, "_", 2)
		matchID, _ := strconv.Atoi(parts[0])
		sourceThreadID := ""
		if len(parts) == 2 {
			sourceThreadID = parts[1]
		}

		_, _ = execDB("UPDATE matches SET status = 'finished' WHERE id = ?", matchID)

		// TODO: обновить статистику каждого игрока в таблице users (kills, deaths, assists, wins, losses, games)

		_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Результат матча #%d подтверждён! Статистика записана.", matchID),
				Embeds: i.Message.Embeds,
				Components: []discordgo.MessageComponent{},
			},
		})

		// Отправить в историю-игр
		channels, _ := s.GuildChannels(i.GuildID)
		for _, c := range channels {
			if c.Name == "📊│история-игр" && c.Type == discordgo.ChannelTypeGuildText {
				if len(i.Message.Embeds) > 0 {
					historyEmbed := *i.Message.Embeds[0]
					historyEmbed.Title = fmt.Sprintf("✅ Матч #%d — завершён", matchID)
					historyEmbed.Color = 0x2ecc71
					_, _ = s.ChannelMessageSendComplex(c.ID, &discordgo.MessageSend{
						Embeds: []*discordgo.MessageEmbed{&historyEmbed},
					})
				}
				break
			}
		}

		// Удалить ветку/канал, куда кидали финальный результат
		if sourceThreadID != "" {
			_, _ = s.ChannelDelete(sourceThreadID)
		}
		return
	}

	// Map ban select
	if strings.HasPrefix(customID, "ban_") {
		matchIDStr := strings.TrimPrefix(customID, "ban_")
		matchID, _ := strconv.Atoi(matchIDStr)
		selectedMap := i.MessageComponentData().Values[0]

		// Find the pick/ban state
		state, ok := activePickBans[i.ChannelID]
		if !ok {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Нет активного распика в этом канале.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		// Check if it's the correct captain's turn
		allowedCapID := state.TCapID
		if state.Turn == "CT" {
			allowedCapID = state.CTCapID
		}
		if i.Member.User.ID != allowedCapID {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Сейчас не твой ход!",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		// Ban the map
		state.BannedMaps = append(state.BannedMaps, selectedMap)

		// Switch turn
		if state.Turn == "T" {
			state.Turn = "CT"
		} else {
			state.Turn = "T"
		}
		state.BanVersion++

		remaining := getRemainingMaps(state.BannedMaps)

		// If 1 map left — match starts
		if len(remaining) == 1 {
			finalMap := remaining[0]
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Content:    "",
					Embeds:     []*discordgo.MessageEmbed{},
					Components: []discordgo.MessageComponent{},
				},
			})
			finalizeMatchStart(s, state, finalMap)
			return
		}

		// Update the embed with new state
		embed := buildPickBanEmbed(state)
		options := buildMapSelectOptions(state)

		placeholder := fmt.Sprintf("%s: забань карту", state.Turn)
		components := []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.SelectMenu{
						CustomID:    fmt.Sprintf("ban_%d", matchID),
						Placeholder: placeholder,
						Options:     options,
					},
				},
			},
		}

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseUpdateMessage,
			Data: &discordgo.InteractionResponseData{
				Embeds:     []*discordgo.MessageEmbed{embed},
				Components: components,
			},
		})
		scheduleAutoBan(s, state)
		return
	}

	// Login button (для уже зарегистрированных)
	if customID == "login_existing" {
		var isRegistered int
		var gameNick string
		_ = queryRowDB("SELECT is_registered, COALESCE(NULLIF(game_nick,''),username) FROM users WHERE discord_id = ?", i.Member.User.ID).Scan(&isRegistered, &gameNick)

		if isRegistered != 1 {
			respondEphemeral(s, i, "❌ Ты не зарегистрирован. Нажми **Регистрация**.")
			return
		}

		uid, _ := getOrCreateUser(i.Member.User.ID, i.Member.User.Username)

		// Восстановить роль Default League и убрать No registred
		roles, _ := s.GuildRoles(i.GuildID)
		for _, r := range roles {
			if r.Name == "Default League" {
				_ = s.GuildMemberRoleAdd(i.GuildID, i.Member.User.ID, r.ID)
			}
			if r.Name == "No registred" {
				_ = s.GuildMemberRoleRemove(i.GuildID, i.Member.User.ID, r.ID)
			}
		}

		// Восстановить ник
		guild, _ := s.Guild(i.GuildID)
		if guild != nil && guild.OwnerID != i.Member.User.ID {
			_ = s.GuildMemberNickname(i.GuildID, i.Member.User.ID, buildServerNick(uid, gameNick))
		}

		respondEphemeral(s, i, fmt.Sprintf("✅ Добро пожаловать обратно! Твой ник: **%s**", buildServerNick(uid, gameNick)))
		return
	}

	// Registration button
	if customID == "open_register" {
		var isRegistered int
		_ = queryRowDB("SELECT is_registered FROM users WHERE discord_id = ?", i.Member.User.ID).Scan(&isRegistered)
		if isRegistered == 1 {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "✅ Вы уже зарегистрированы. Для смены ника используйте /nickname",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseModal,
			Data: &discordgo.InteractionResponseData{
				CustomID: "modal_register",
				Title:    "Регистрация",
				Components: []discordgo.MessageComponent{
					discordgo.ActionsRow{Components: []discordgo.MessageComponent{
						discordgo.TextInput{CustomID: "register_nick", Label: "Игровой ник", Style: discordgo.TextInputShort, Placeholder: "Например: Merrzzik", Required: true},
					}},
					discordgo.ActionsRow{Components: []discordgo.MessageComponent{
						discordgo.TextInput{CustomID: "register_game_id", Label: "Игровой ID", Style: discordgo.TextInputShort, Placeholder: "Например: 126512660", Required: true},
					}},
				},
			},
		})
		return
	}

	// Host ID button
	if strings.HasPrefix(customID, "hostid_") {
		parts := strings.SplitN(customID, "_", 3)
		hostDiscordID := ""
		if len(parts) == 3 {
			hostDiscordID = parts[2]
		}
		hostGameID := ""
		_ = queryRowDB("SELECT game_id FROM users WHERE discord_id = ?", hostDiscordID).Scan(&hostGameID)
		if hostGameID == "" {
			hostGameID = "не указан"
		}
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("🆔 ID хоста: **%s**", hostGameID),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
		return
	}

	// Send result button
	if customID == "send_result" {
		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseModal,
			Data: &discordgo.InteractionResponseData{
				CustomID: "modal_send_result",
				Title:    "Отправка результата",
				Components: []discordgo.MessageComponent{
					discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							discordgo.TextInput{
								CustomID:    "match_id_input",
								Label:       "ID матча",
								Style:       discordgo.TextInputShort,
								Placeholder: "Например: 1042",
								Required:    true,
							},
						},
					},
				},
			},
		})
		return
	}
}

// ── Helpers ────────────────────────────────────────────────────────────

// getModalValue безопасно извлекает значение TextInput из модалки по customID поля
func getModalValue(data discordgo.ModalSubmitInteractionData, customID string) string {
	for _, row := range data.Components {
		ar, ok := row.(*discordgo.ActionsRow)
		if !ok {
			continue
		}
		for _, comp := range ar.Components {
			ti, ok := comp.(*discordgo.TextInput)
			if ok && ti.CustomID == customID {
				return ti.Value
			}
		}
	}
	return ""
}

func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// ── Modal Submit ────────────────────────────────────────────────────────

func handleModal(s *discordgo.Session, i *discordgo.InteractionCreate) {
	data := i.ModalSubmitData()

	if data.CustomID == "modal_register" {
		nick   := getModalValue(data, "register_nick")
		gameID := getModalValue(data, "register_game_id")

		if nick == "" {
			respondEphemeral(s, i, "❌ Ник не может быть пустым.")
			return
		}

		uid, err := getOrCreateUser(i.Member.User.ID, i.Member.User.Username)
		if err != nil {
			log.Println("register getOrCreateUser error:", err)
			respondEphemeral(s, i, "❌ Ошибка базы данных. Попробуй ещё раз.")
			return
		}

		_, err = execDB(
			"UPDATE users SET username = ?, game_nick = ?, game_id = ?, is_registered = 1 WHERE discord_id = ?",
			nick, nick, gameID, i.Member.User.ID,
		)
		if err != nil {
			log.Println("register db update error:", err)
			respondEphemeral(s, i, "❌ Ошибка сохранения данных. Попробуй ещё раз.")
			return
		}

		roles, _ := s.GuildRoles(i.GuildID)
		for _, r := range roles {
			if r.Name == "Default League" {
				if addErr := s.GuildMemberRoleAdd(i.GuildID, i.Member.User.ID, r.ID); addErr != nil {
					log.Println("register add role error:", addErr)
				}
			}
			if r.Name == "No registred" {
				_ = s.GuildMemberRoleRemove(i.GuildID, i.Member.User.ID, r.ID)
			}
		}

		guild, _ := s.Guild(i.GuildID)
		if guild != nil && guild.OwnerID != i.Member.User.ID {
			if nickErr := s.GuildMemberNickname(i.GuildID, i.Member.User.ID, buildServerNick(uid, nick)); nickErr != nil {
				log.Println("register set nick error:", nickErr)
			}
		}

		msg := fmt.Sprintf("✅ Регистрация завершена!\n🎮 Серверный ник: **%s**\n🆔 Игровой ID: **%s**\n\nТеперь тебе доступна Default League. Другие лиги открываются по роли.", buildServerNick(uid, nick), gameID)
		respondEphemeral(s, i, msg)
		return
	}

	if data.CustomID == "modal_send_result" {
		matchIDStr := getModalValue(data, "match_id_input")
		matchID, err := strconv.Atoi(matchIDStr)
		if err != nil {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Неверный ID матча.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		var status string
		err = queryRowDB("SELECT status FROM matches WHERE id = ?", matchID).Scan(&status)
		if err != nil {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Матч не найден.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		thread, err := s.MessageThreadStartComplex(i.ChannelID, i.Message.ID, &discordgo.ThreadStart{
			Name:                fmt.Sprintf("Отчет матча #%d", matchID),
			AutoArchiveDuration: 60,
		})
		if err != nil {
			thread, err = s.ThreadStartComplex(i.ChannelID, &discordgo.ThreadStart{
				Name:                fmt.Sprintf("Отчет матча #%d", matchID),
				AutoArchiveDuration: 60,
				Type:                discordgo.ChannelTypeGuildPublicThread,
			})
		}

		if err != nil {
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "❌ Ошибка создания ветки.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		_, _ = s.ChannelMessageSend(thread.ID, fmt.Sprintf("<@%s>, отправьте финальный скриншот матча #%d сюда.\nБот автоматически обработает статистику.", i.Member.User.ID, matchID))
		_, _ = execDB("UPDATE matches SET status = 'reporting' WHERE id = ?", matchID)

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Ветка создана: <#%s>", thread.ID),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	}
}
