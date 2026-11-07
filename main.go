package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
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
	"unicode"

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

const commandsChannelName = "🤖│команды"

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
			Description: "Карточка игрока: рейтинг, карты, последние матчи",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Чей профиль; без него — свой",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "id",
					Description: "Игровой ID из Standoff 2 или номер на проекте (#123)",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "league",
					Description: "Статистика какой лиги; без неё — своей",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{Name: "Pro", Value: "Pro League"},
						{Name: "Division", Value: "Division League"},
						{Name: "Prospect", Value: "Prospect League"},
						{Name: "Default", Value: "Default League"},
					},
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
		commandsChannelName: true,
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

	// Удаляем старое сообщение пик-бана
	if state.MessageID != "" {
		_ = s.ChannelMessageDelete(state.ChannelID, state.MessageID)
	}

	hostMention := fmt.Sprintf("<@%s>", state.TCapID)
	mentions := ""
	for _, p := range append(state.TTeam, state.CTTeam...) { mentions += fmt.Sprintf("<@%s> ", p.DiscordID) }

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Отправить ссылку на лобби", Style: discordgo.SuccessButton, CustomID: fmt.Sprintf("lobby_link_%d", state.MatchID), Emoji: &discordgo.ComponentEmoji{Name: "🔗"}},
			discordgo.Button{Label: "Получить ID хоста", Style: discordgo.PrimaryButton, CustomID: fmt.Sprintf("hostid_%d_%s", state.MatchID, state.TCapID), Emoji: &discordgo.ComponentEmoji{Name: "🆔"}},
			discordgo.Button{Label: "Игрок не зашёл (0/6)", Style: discordgo.DangerButton, CustomID: fmt.Sprintf("noshow_%d", state.MatchID), Emoji: &discordgo.ComponentEmoji{Name: "🚫"}},
		}},
	}

	// Генерируем PNG-карточку распика
	cardPNG, cardErr := renderPickCardPNG(s, state, finalMap)
	if cardErr == nil && len(cardPNG) > 0 {
		matchEmbed := &discordgo.MessageEmbed{
			Color:  0x2d2d35,
			Image:  &discordgo.MessageEmbedImage{URL: "attachment://pick-card.png"},
			Footer: &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("ID матча для отправки результата: %d • %s", state.MatchID, time.Now().Format("02.01.2006 15:04"))},
		}
		_, _ = s.ChannelMessageSendComplex(state.ChannelID, &discordgo.MessageSend{
			Content:    mentions,
			Embeds:     []*discordgo.MessageEmbed{matchEmbed},
			Components: components,
			Files:      []*discordgo.File{{Name: "pick-card.png", ContentType: "image/png", Reader: bytes.NewReader(cardPNG)}},
		})
	} else {
		// Фоллбэк: текстовый embed если карточка не сгенерировалась
		embed := &discordgo.MessageEmbed{
			Title:       fmt.Sprintf("#%d · %s %s", state.MatchID, finalMap.Emoji, finalMap.Name),
			Description: "Игра идёт до **13 раундов**\n**Комнаты:** 🔒 Нет доступа · 🔒 Нет доступа — зайти может только свой состав\n*Ждём ссылку на лобби от хоста...*",
			Color:       0xe91e63,
			Footer:      &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("ID матча для отправки результата: %d • %s", state.MatchID, time.Now().Format("02.01.2006 15:04"))},
		}
		_, _ = s.ChannelMessageSendComplex(state.ChannelID, &discordgo.MessageSend{Content: mentions, Embeds: []*discordgo.MessageEmbed{embed}, Components: components})
	}
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

func findCommandsChannelID(s *discordgo.Session, guildID string) string {
	channels, err := s.GuildChannels(guildID)
	if err != nil {
		return ""
	}
	for _, ch := range channels {
		if ch.Type == discordgo.ChannelTypeGuildText && ch.Name == commandsChannelName {
			return ch.ID
		}
	}
	return ""
}

func commandAllowedInChannel(s *discordgo.Session, i *discordgo.InteractionCreate, commandName string) bool {
	cmdChannelID := findCommandsChannelID(s, i.GuildID)
	if cmdChannelID == "" {
		return commandName == "setup"
	}
	if i.ChannelID == cmdChannelID {
		return true
	}
	respondEphemeral(s, i, fmt.Sprintf("❌ Команды можно использовать только в канале <#%s>.", cmdChannelID))
	return false
}

func handleCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	commandName := i.ApplicationCommandData().Name
	if !commandAllowedInChannel(s, i, commandName) {
		return
	}
	switch commandName {
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

		denyAll            := int64(discordgo.PermissionViewChannel)
		viewOnly           := int64(discordgo.PermissionViewChannel | discordgo.PermissionReadMessageHistory)
		denyLeagueAccess   := int64(discordgo.PermissionSendMessages | discordgo.PermissionVoiceConnect |
			discordgo.PermissionVoiceSpeak | discordgo.PermissionVoiceUseVAD)
		fullAccess         := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages |
			discordgo.PermissionReadMessageHistory | discordgo.PermissionVoiceConnect |
			discordgo.PermissionVoiceSpeak | discordgo.PermissionVoiceUseVAD)
		pubWrite           := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionReadMessageHistory)

		applyLeagueCatPerms := func(catID, ownerRole string) {
			_ = s.ChannelPermissionSet(catID, guildID, discordgo.PermissionOverwriteTypeRole, 0, denyAll)
			if noRegRoleID != "" { _ = s.ChannelPermissionSet(catID, noRegRoleID, discordgo.PermissionOverwriteTypeRole, 0, denyAll) }
			for roleName, rid := range leagueRoleIDs {
				if rid == "" {
					continue
				}
				allow := viewOnly
				deny := denyLeagueAccess
				if roleName == ownerRole {
					allow = fullAccess
					deny = 0
				}
				_ = s.ChannelPermissionSet(catID, rid, discordgo.PermissionOverwriteTypeRole, allow, deny)
			}
			if gameAdminRoleID != "" { _ = s.ChannelPermissionSet(catID, gameAdminRoleID, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
			if tickAdminRoleID != "" { _ = s.ChannelPermissionSet(catID, tickAdminRoleID, discordgo.PermissionOverwriteTypeRole, fullAccess, 0) }
		}

		applyRankedPerms := func(id string) {
			_ = s.ChannelPermissionDelete(id, guildID)
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
			cmdChannel, cmdCreated := ensureText(commandsChannelName, resCat.ID)
			if cmdChannel != nil && cmdCreated {
				_, _ = s.ChannelMessageSendComplex(cmdChannel.ID, &discordgo.MessageSend{
					Embeds: []*discordgo.MessageEmbed{{
						Title:       "🤖 КАНАЛ КОМАНД",
						Color:       0x6c5ce7,
						Description: "Используйте slash-команды только здесь.\n\n› /profile — карточка игрока\n› /nickname — смена игрового ника\n› /setup и /delete — управление структурой сервера",
						Footer:      &discordgo.MessageEmbedFooter{Text: "ELITE FPL • commands"},
					}},
				})
			}
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
				if noRegRoleID != "" { _ = s.GuildMemberRoleRemove(guildID, m.User.ID, noRegRoleID) }
				var nick string
				_ = queryRowDB("SELECT COALESCE(NULLIF(game_nick,''),username) FROM users WHERE discord_id=?", m.User.ID).Scan(&nick)
				g, _ := s.Guild(guildID)
				if g != nil && g.OwnerID != m.User.ID { _ = s.GuildMemberNickname(guildID, m.User.ID, buildServerNick(uid, nick)) }
			} else {
				if noRegRoleID != "" { _ = s.GuildMemberRoleAdd(guildID, m.User.ID, noRegRoleID) }
				for _, rid := range leagueRoleIDs {
					if rid != "" {
						_ = s.GuildMemberRoleRemove(guildID, m.User.ID, rid)
					}
				}
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
	// Сразу отвечаем Discord что бот думает (до 15 минут на ответ)
	_ = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	})

	targetUser := i.Member.User
	lookupID := ""
	preferredLeague := ""
	options := i.ApplicationCommandData().Options
	for _, opt := range options {
		switch opt.Name {
		case "user":
			targetUser = opt.UserValue(s)
		case "id":
			lookupID = strings.TrimSpace(opt.StringValue())
		case "league":
			preferredLeague = opt.StringValue()
		}
	}

	editError := func(msg string) {
		_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
			Content: &msg,
		})
	}

	if lookupID != "" {
		lookupValue := strings.TrimPrefix(lookupID, "#")
		targetDiscordID := ""
		if internalID, err := strconv.Atoi(lookupValue); err == nil {
			_ = queryRowDB("SELECT discord_id FROM users WHERE id = ?", internalID).Scan(&targetDiscordID)
		}
		if targetDiscordID == "" {
			_ = queryRowDB("SELECT discord_id FROM users WHERE game_id = ?", lookupValue).Scan(&targetDiscordID)
		}
		if targetDiscordID == "" {
			editError("❌ Игрок по указанному ID не найден.")
			return
		}
		if user, err := s.User(targetDiscordID); err == nil && user != nil {
			targetUser = user
		} else {
			var fallbackName string
			_ = queryRowDB("SELECT COALESCE(NULLIF(game_nick,''), username) FROM users WHERE discord_id = ?", targetDiscordID).Scan(&fallbackName)
			targetUser = &discordgo.User{ID: targetDiscordID, Username: fallbackName}
		}
	}

	cardPNG, err := renderProfileCardPNG(s, i.GuildID, targetUser, preferredLeague)
	if err != nil {
		editError(fmt.Sprintf("❌ Не удалось собрать карточку профиля: %v", err))
		return
	}

	// Отправляем карточку через followup (файлы нельзя отправить через InteractionResponseEdit)
	_, _ = s.FollowupMessageCreate(i.Interaction, true, &discordgo.WebhookParams{
		Embeds: []*discordgo.MessageEmbed{{
			Title: fmt.Sprintf("Профиль — %s", targetUser.Username),
			Color: 0x6c5ce7,
			Image: &discordgo.MessageEmbedImage{URL: "attachment://profile-card.png"},
		}},
		Files: []*discordgo.File{{
			Name:        "profile-card.png",
			ContentType: "image/png",
			Reader:      bytes.NewReader(cardPNG),
		}},
	})

	// Удаляем начальное "бот думает" сообщение
	emptyContent := "⠀"
	_, _ = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &emptyContent})
}

// ── Component (buttons, selects) ────────────────────────────────────────

func handleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	customID := i.MessageComponentData().CustomID

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

	// Confirm match result
	if strings.HasPrefix(customID, "confirm_result_") {
		matchIDStr := strings.TrimPrefix(customID, "confirm_result_")
		matchID, _ := strconv.Atoi(matchIDStr)

		// Update match status to finished
		_, _ = execDB("UPDATE matches SET status = 'finished' WHERE id = ?", matchID)

		// Update player stats in users table
		pRows, pErr := queryDB("SELECT user_id, kills, deaths, assists, team FROM match_players WHERE match_id = ?", matchID)
		if pErr == nil {
			defer pRows.Close()
			var scoreCT, scoreT int
			_ = queryRowDB("SELECT score_ct, score_t FROM matches WHERE id = ?", matchID).Scan(&scoreCT, &scoreT)
			for pRows.Next() {
				var uid, k, d, a int
				var team string
				pRows.Scan(&uid, &k, &d, &a, &team)
				won := 0
				if (team == "CT" && scoreCT > scoreT) || (team == "T" && scoreT > scoreCT) { won = 1 }
				lost := 0
				if won == 0 { lost = 1 }
				_, _ = execDB("UPDATE users SET games = games + 1, wins = wins + ?, losses = losses + ?, kills = kills + ?, deaths = deaths + ?, assists = assists + ? WHERE id = ?",
					won, lost, k, d, a, uid)
			}
		}
		_ = updateMatchDerivedStats(matchID)

		respondEphemeral(s, i, fmt.Sprintf("✅ Матч #%d подтверждён! Статистика записана.", matchID))

		// Send generated match card to история-игр
		historyContent := fmt.Sprintf("✅ #%d", matchID)
		mentions := []string{}
		mentionRows, mentionErr := queryDB("SELECT u.discord_id FROM match_players mp JOIN users u ON mp.user_id = u.id WHERE mp.match_id = ? ORDER BY mp.id", matchID)
		if mentionErr == nil {
			defer mentionRows.Close()
			for mentionRows.Next() {
				var discordID string
				if err := mentionRows.Scan(&discordID); err == nil && discordID != "" {
					mentions = append(mentions, fmt.Sprintf("<@%s>", discordID))
				}
			}
		}
		if len(mentions) > 0 {
			historyContent += "\n" + strings.Join(mentions, ", ")
		}
		matchCardPNG, cardErr := renderMatchCardPNG(s, matchID)

		channels, _ := s.GuildChannels(i.GuildID)
		for _, c := range channels {
			if c.Name != "📊│история-игр" {
				continue
			}
			if cardErr == nil && len(matchCardPNG) > 0 {
				_, _ = s.ChannelMessageSendComplex(c.ID, &discordgo.MessageSend{
					Content: historyContent,
					Embeds: []*discordgo.MessageEmbed{{
						Title: fmt.Sprintf("🏆 Матч #%d завершён", matchID),
						Color: 0x2ecc71,
						Image: &discordgo.MessageEmbedImage{URL: "attachment://match-card.png"},
						Footer: &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("ELITE FPL • результат матча • %s", time.Now().Format("02.01.2006 15:04"))},
					}},
					Files: []*discordgo.File{{
						Name:        "match-card.png",
						ContentType: "image/png",
						Reader:      bytes.NewReader(matchCardPNG),
					}},
				})
			} else {
				_, _ = s.ChannelMessageSend(c.ID, historyContent)
			}
			break
		}

		// Автоудаление тредов «Отчет матча #N» после подтверждения
		go func() {
			time.Sleep(3 * time.Second)
			allChannels, err := s.GuildChannels(i.GuildID)
			if err != nil { return }
			threadName := fmt.Sprintf("Отчет матча #%d", matchID)
			for _, ch := range allChannels {
				if ch.Name == threadName && (ch.Type == discordgo.ChannelTypeGuildPublicThread || ch.Type == discordgo.ChannelTypeGuildPrivateThread) {
					_, _ = s.ChannelDelete(ch.ID)
				}
			}
			// Also check active threads
			activeThreads, err := s.GuildThreadsActive(i.GuildID)
			if err == nil {
				for _, t := range activeThreads.Threads {
					if t.Name == threadName {
						_, _ = s.ChannelDelete(t.ID)
					}
				}
			}
		}()

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
					Content: "❌ Матч не найден в базе данных.",
					Flags:   discordgo.MessageFlagsEphemeral,
				},
			})
			return
		}

		if status == "finished" {
			respondEphemeral(s, i, "❌ Этот матч уже зарегистрирован и подтверждён.")
			return
		}
		if status == "review" {
			respondEphemeral(s, i, "❌ Результат этого матча уже на проверке у администрации.")
			return
		}
		if status == "reporting" {
			respondEphemeral(s, i, "❌ Для этого матча уже создана ветка отправки результата. Проверьте список активных веток.")
			return
		}

		// Создаём приватный тред (видят только приглашённые)
		thread, err := s.ThreadStartComplex(i.ChannelID, &discordgo.ThreadStart{
			Name:                fmt.Sprintf("Отчет матча #%d", matchID),
			AutoArchiveDuration: 60,
			Type:                discordgo.ChannelTypeGuildPrivateThread,
			Invitable:           false,
		})
		if err != nil {
			// Фоллбэк: приватный тред от сообщения
			thread, err = s.MessageThreadStartComplex(i.ChannelID, i.Message.ID, &discordgo.ThreadStart{
				Name:                fmt.Sprintf("Отчет матча #%d", matchID),
				AutoArchiveDuration: 60,
				Type:                discordgo.ChannelTypeGuildPrivateThread,
				Invitable:           false,
			})
		}
		if err != nil {
			// Последний фоллбэк: публичный тред (если нет Boost Level 2)
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

// ── AI Screenshot Recognition ───────────────────────────────────────────

func onMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author == nil || m.Author.Bot { return }
	if len(m.Attachments) == 0 { return }

	// Check if this is a match report thread
	ch, err := s.Channel(m.ChannelID)
	if err != nil || (ch.Type != discordgo.ChannelTypeGuildPublicThread && ch.Type != discordgo.ChannelTypeGuildPrivateThread) { return }
	if !strings.HasPrefix(ch.Name, "Отчет матча #") { return }

	// Extract match ID from thread name
	matchIDStr := strings.TrimPrefix(ch.Name, "Отчет матча #")
	matchID, err := strconv.Atoi(matchIDStr)
	if err != nil { return }

	// Get first image attachment
	imageURL := ""
	for _, att := range m.Attachments {
		if strings.HasSuffix(strings.ToLower(att.Filename), ".png") ||
			strings.HasSuffix(strings.ToLower(att.Filename), ".jpg") ||
			strings.HasSuffix(strings.ToLower(att.Filename), ".jpeg") ||
			strings.HasSuffix(strings.ToLower(att.Filename), ".webp") {
			imageURL = att.URL
			break
		}
	}
	if imageURL == "" { return }

	_, _ = s.ChannelMessageSend(m.ChannelID, "🔍 Обрабатываю скриншот...")

	// Get match info from DB
	var mapName, category string
	_ = queryRowDB("SELECT COALESCE(map_name,''), COALESCE(category,'') FROM matches WHERE id = ?", matchID).Scan(&mapName, &category)

	// Get match players
	rows, err := queryDB("SELECT mp.user_id, mp.team, COALESCE(NULLIF(u.game_nick, ''), u.username), u.discord_id FROM match_players mp JOIN users u ON mp.user_id = u.id WHERE mp.match_id = ? ORDER BY mp.id", matchID)
	if err != nil { _, _ = s.ChannelMessageSend(m.ChannelID, "❌ Ошибка получения игроков матча."); return }
	defer rows.Close()

	var players []MatchPlayerInfo
	for rows.Next() {
		var p MatchPlayerInfo
		rows.Scan(&p.UserID, &p.Team, &p.Username, &p.DiscordID)
		players = append(players, p)
	}

	// Call AI to recognize screenshot
	result, err := recognizeScreenshot(imageURL, players)
	if err != nil {
		if strings.Contains(err.Error(), "NOT_SCOREBOARD") {
			_, _ = s.ChannelMessageSend(m.ChannelID, "❌ Это не финальный скриншот матча Standoff 2. Отправьте именно финальную таблицу результатов (экран после завершения матча с надписью «СТАТИСТИКА» / «ПОБЕДА» / «ПОРАЖЕНИЕ»).")
			return
		}
		_, _ = s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("❌ Ошибка распознавания: %v", err))
		return
	}
	result = normalizeRecognition(players, result)

	// Save recognized stats to DB
	for _, p := range result.Players {
		rating := calculatePlayerRating(p.Kills, p.Deaths, p.Assists)
		_, _ = execDB("UPDATE match_players SET kills = ?, deaths = ?, assists = ?, rating = ? WHERE match_id = ? AND user_id = ?",
			p.Kills, p.Deaths, p.Assists, rating, matchID, p.UserID)
	}
	scoreParts := strings.Split(result.Score, ":")
	if len(scoreParts) == 2 {
		sct, _ := strconv.Atoi(strings.TrimSpace(result.ScoreCT))
		st, _ := strconv.Atoi(strings.TrimSpace(result.ScoreT))
		_, _ = execDB("UPDATE matches SET score_ct = ?, score_t = ?, status = 'review' WHERE id = ?", sct, st, matchID)
	}

	_, _ = s.ChannelMessageSend(m.ChannelID, "✅ Скриншот распознан! Отправляю на проверку...")

	// Find admin channel
	channels, _ := s.GuildChannels(m.GuildID)
	adminChID := ""
	for _, c := range channels {
		if c.Name == "📁│обычный-рег" { adminChID = c.ID; break }
	}
	if adminChID == "" { _, _ = s.ChannelMessageSend(m.ChannelID, "❌ Канал обычный-рег не найден."); return }

	mentions := []string{}
	for _, p := range result.Players {
		if p.DiscordID != "" {
			mentions = append(mentions, fmt.Sprintf("<@%s>", p.DiscordID))
		}
	}
	content := fmt.Sprintf("✅ #%d", matchID)
	if len(mentions) > 0 {
		content += "\n" + strings.Join(mentions, ", ")
	}

	// === На проверку уходит ИСХОДНЫЙ скриншот + embed с распознанными данными ===
	teamAText := ""
	teamBText := ""
	for _, p := range result.Players {
		line := fmt.Sprintf("• <@%s> — %d/%d/%d\n", p.DiscordID, p.Kills, p.Assists, p.Deaths)
		if p.Team == "CT" { teamAText += line } else { teamBText += line }
	}
	if teamAText == "" { teamAText = "Нет данных\n" }
	if teamBText == "" { teamBText = "Нет данных\n" }

	reviewEmbed := &discordgo.MessageEmbed{
		Title: fmt.Sprintf("🏆 Матч #%d — на проверку", matchID),
		Description: fmt.Sprintf(
			"**Отправил:** <@%s>\n**Карта:** %s %s\n**Счёт:** %s\n\n"+
			"🅰️ **CT — %s · K/A/D**\n%s\n"+
			"🅱️ **T — %s · K/A/D**\n%s",
			m.Author.ID, getMapEmojiByName(mapName), mapName, result.Score,
			result.ScoreCT, teamAText, result.ScoreT, teamBText,
		),
		Color: 0xe91e63,
		Image: &discordgo.MessageEmbedImage{URL: imageURL},
		Footer: &discordgo.MessageEmbedFooter{Text: fmt.Sprintf("Матч #%d • %s", matchID, time.Now().Format("02.01.2006 15:04"))},
	}

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			discordgo.Button{Label: "Подтвердить результат", Style: discordgo.SuccessButton, CustomID: fmt.Sprintf("confirm_result_%d", matchID), Emoji: &discordgo.ComponentEmoji{Name: "✅"}},
			discordgo.Button{Label: "Счёт", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("edit_score_%d", matchID), Emoji: &discordgo.ComponentEmoji{Name: "📝"}},
			discordgo.Button{Label: "Статистика", Style: discordgo.SecondaryButton, CustomID: fmt.Sprintf("edit_stats_%d", matchID), Emoji: &discordgo.ComponentEmoji{Name: "📊"}},
		}},
	}

	_, _ = s.ChannelMessageSendComplex(adminChID, &discordgo.MessageSend{
		Content:    content,
		Embeds:     []*discordgo.MessageEmbed{reviewEmbed},
		Components: components,
	})
}

func getMapEmojiByName(name string) string {
	for _, m := range Maps { if m.Name == name { return m.Emoji } }
	return "🗺️"
}

type RecognizedPlayer struct {
	DiscordID string
	Username  string
	UserID    int
	Team      string
	Kills     int
	Deaths    int
	Assists   int
}

type RecognitionResult struct {
	Score   string
	ScoreCT string
	ScoreT  string
	Players []RecognizedPlayer
}

type MatchPlayerInfo struct {
	UserID    int
	Team      string
	Username  string
	DiscordID string
}

func stripLeadingClanTags(value string) string {
	text := strings.TrimSpace(value)
	text = strings.TrimLeft(text, "!?.@#$%^&*()_+-=[]{}\\|/`~,:;\"' ")

	if idx := strings.IndexAny(text, "])}"); strings.HasPrefix(text, "[") || strings.HasPrefix(text, "(") || strings.HasPrefix(text, "{") {
		if idx > 0 && idx < 24 {
			text = strings.TrimSpace(text[idx+1:])
		}
	}

	for _, sep := range []string{"|", "｜", "¦", "—", "–", "-", "·", "•", ":"} {
		idx := strings.Index(text, sep)
		if idx > 0 && idx < 24 {
			left := strings.TrimSpace(text[:idx])
			right := strings.TrimSpace(text[idx+len(sep):])
			if left != "" && right != "" {
				text = right
				break
			}
		}
	}

	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "OLD ") || upper == "OLD" {
		text = strings.TrimSpace(text[3:])
	}

	return strings.TrimSpace(text)
}

func normalizeNickname(value string) string {
	replacer := strings.NewReplacer(
		"а", "a", "б", "b", "в", "v", "г", "g", "д", "d", "е", "e", "ё", "e",
		"ж", "zh", "з", "z", "и", "i", "й", "y", "к", "k", "л", "l", "м", "m",
		"н", "n", "о", "o", "п", "p", "р", "r", "с", "s", "т", "t", "у", "u",
		"ф", "f", "х", "h", "ц", "c", "ч", "ch", "ш", "sh", "щ", "sch", "ъ", "",
		"ы", "y", "ь", "", "э", "e", "ю", "yu", "я", "ya",
	)

	text := strings.ToLower(stripLeadingClanTags(value))
	text = replacer.Replace(text)

	var builder strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func nicknameSimilarity(first, second string) float64 {
	left := normalizeNickname(first)
	right := normalizeNickname(second)
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	if strings.Contains(left, right) || strings.Contains(right, left) {
		return 0.95
	}
	if len(left) < 2 || len(right) < 2 {
		return 0
	}

	leftBigrams := map[string]int{}
	rightBigrams := map[string]int{}
	leftCount := 0
	rightCount := 0

	for i := 0; i < len(left)-1; i++ {
		leftBigrams[left[i:i+2]]++
		leftCount++
	}
	for i := 0; i < len(right)-1; i++ {
		rightBigrams[right[i:i+2]]++
		rightCount++
	}
	if leftCount == 0 || rightCount == 0 {
		return 0
	}

	intersections := 0
	for gram, leftValue := range leftBigrams {
		rightValue := rightBigrams[gram]
		if rightValue == 0 {
			continue
		}
		if leftValue < rightValue {
			intersections += leftValue
		} else {
			intersections += rightValue
		}
	}

	return (2 * float64(intersections)) / float64(leftCount+rightCount)
}

func normalizeRecognition(matchPlayers []MatchPlayerInfo, result *RecognitionResult) *RecognitionResult {
	if result == nil {
		return nil
	}

	used := map[int]bool{}
	normalizedPlayers := make([]RecognizedPlayer, 0, len(matchPlayers))

	findBestMatch := func(player MatchPlayerInfo) *RecognizedPlayer {
		bestIndex := -1
		bestScore := 0.0

		for idx, recognized := range result.Players {
			if used[idx] {
				continue
			}

			score := 0.0
			if recognized.UserID != 0 && recognized.UserID == player.UserID {
				score = 100
			} else if recognized.DiscordID != "" && recognized.DiscordID == player.DiscordID {
				score = 90
			} else {
				score = nicknameSimilarity(player.Username, recognized.Username)
				if strings.EqualFold(recognized.Team, player.Team) {
					score += 0.05
				}
				if score < 0.72 {
					continue
				}
			}

			if score > bestScore {
				bestScore = score
				bestIndex = idx
			}
		}

		if bestIndex < 0 {
			return nil
		}

		used[bestIndex] = true
		clone := result.Players[bestIndex]
		return &clone
	}

	for _, player := range matchPlayers {
		kills := 0
		assists := 0
		deaths := 13

		if matched := findBestMatch(player); matched != nil {
			kills = matched.Kills
			assists = matched.Assists
			deaths = matched.Deaths
			if kills == 0 && assists == 0 && deaths == 0 {
				deaths = 13
			}
		}

		normalizedPlayers = append(normalizedPlayers, RecognizedPlayer{
			DiscordID: player.DiscordID,
			Username:  player.Username,
			UserID:    player.UserID,
			Team:      player.Team,
			Kills:     kills,
			Deaths:    deaths,
			Assists:   assists,
		})
	}

	return &RecognitionResult{
		Score:   result.Score,
		ScoreCT: result.ScoreCT,
		ScoreT:  result.ScoreT,
		Players: normalizedPlayers,
	}
}

func extractHistoryCardURL(message *discordgo.Message) string {
	if message == nil {
		return ""
	}
	for _, att := range message.Attachments {
		if att != nil && att.URL != "" {
			return att.URL
		}
	}
	for _, embed := range message.Embeds {
		if embed != nil && embed.Image != nil && embed.Image.URL != "" {
			return embed.Image.URL
		}
	}
	return ""
}

func downloadURLBytes(fileURL string) ([]byte, string, error) {
	resp, err := http.Get(fileURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return body, resp.Header.Get("Content-Type"), nil
}

func imageExtension(fileURL, contentType string) string {
	lowerURL := strings.ToLower(fileURL)
	lowerType := strings.ToLower(contentType)
	switch {
	case strings.Contains(lowerType, "png") || strings.HasSuffix(lowerURL, ".png"):
		return ".png"
	case strings.Contains(lowerType, "webp") || strings.HasSuffix(lowerURL, ".webp"):
		return ".webp"
	default:
		return ".jpg"
	}
}

func cleanEnvSecret(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "\"'")
	return strings.TrimSpace(value)
}

func buildAIImagePayloadURL(imageURL string) string {
	resp, err := http.Get(imageURL)
	if err != nil {
		return imageURL
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return imageURL
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		return imageURL
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		lowerURL := strings.ToLower(imageURL)
		switch {
		case strings.HasSuffix(lowerURL, ".png"):
			contentType = "image/png"
		case strings.HasSuffix(lowerURL, ".webp"):
			contentType = "image/webp"
		default:
			contentType = "image/jpeg"
		}
	}

	return fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(body))
}

// fetchAvailableModels запрашивает список доступных моделей у AI-провайдера
func fetchAvailableModels(baseURL, apiKey string) []string {
	req, err := http.NewRequest("GET", baseURL+"/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil
	}

	var models []string
	for _, m := range result.Data {
		models = append(models, m.ID)
	}
	return models
}

// selectBestVisionModel выбирает лучшую vision-модель из списка доступных
func selectBestVisionModel(available []string, preferred string) string {
	if len(available) == 0 {
		return preferred
	}

	for _, m := range available {
		if strings.EqualFold(m, preferred) {
			return m
		}
	}

	visionPriority := []string{
		"gemini-3.8-flash",
		"gemini-3.7-flash",
		"gemini-3.6-flash",
		"gemini-3.1-pro",
		"gemini-2.5-flash",
		"gemini-2.0-flash",
		"gemini-1.5-flash",
		"gemini-1.5-pro",
		"gpt-4o-mini",
		"gpt-4o",
		"gpt-4-turbo",
		"gpt-4-vision-preview",
		"gpt-4.1-mini",
		"gpt-4.1",
		"gpt-4.1-nano",
		"gpt-4-turbo-2024-04-09",
		"gpt-4o-2024-05-13",
		"gpt-4o-2024-08-06",
		"gpt-4o-mini-2024-07-18",
		"chatgpt-4o-latest",
	}

	for _, prio := range visionPriority {
		for _, m := range available {
			if strings.EqualFold(m, prio) {
				return m
			}
		}
	}

	for _, m := range available {
		lower := strings.ToLower(m)
		if strings.Contains(lower, "gemini") || strings.Contains(lower, "vision") || strings.Contains(lower, "4o") || strings.Contains(lower, "4.1") || strings.Contains(lower, "flash") || strings.Contains(lower, "pro") {
			return m
		}
	}

	for _, m := range available {
		lower := strings.ToLower(m)
		if strings.Contains(lower, "gpt-4") || strings.Contains(lower, "gemini") {
			return m
		}
	}

	return available[0]
}

func buildModelCandidates(preferred string, available []string, baseURL string) []string {
	seen := map[string]bool{}
	candidates := []string{}
	add := func(model string) {
		model = cleanEnvSecret(model)
		if model == "" || seen[strings.ToLower(model)] {
			return
		}
		seen[strings.ToLower(model)] = true
		candidates = append(candidates, model)
	}

	if len(available) > 0 {
		add(selectBestVisionModel(available, preferred))
		for _, model := range available {
			lower := strings.ToLower(model)
			if strings.Contains(lower, "gemini") || strings.Contains(lower, "vision") || strings.Contains(lower, "4o") || strings.Contains(lower, "flash") || strings.Contains(lower, "pro") {
				add(model)
			}
		}
	}

	lowerBase := strings.ToLower(baseURL)
	if strings.Contains(lowerBase, "starimg") || strings.Contains(strings.ToLower(preferred), "gemini") {
		add("gemini-3.8-flash")
		add("gemini-3.7-flash")
		add("gemini-3.6-flash")
		add("gemini-3.1-pro")
		add("gemini-2.5-flash")
		add("gemini-1.5-flash")
	}

	add(preferred)
	add("gpt-4o-mini")
	add("gpt-4o")
	add("gpt-4-turbo")
	add("gpt-4-vision-preview")
	add("gpt-4.1-mini")
	add("gpt-4.1")

	return candidates
}

func recognizeScreenshot(imageURL string, matchPlayers []MatchPlayerInfo) (*RecognitionResult, error) {
	apiKey := cleanEnvSecret(os.Getenv("AI_API_KEY"))
	baseURL := strings.TrimRight(cleanEnvSecret(os.Getenv("AI_BASE_URL")), "/")
	model := cleanEnvSecret(os.Getenv("AI_MODEL"))
	if model == "" {
		model = "gpt-4o-mini"
	}
	if apiKey == "" || baseURL == "" {
		return nil, fmt.Errorf("AI_API_KEY or AI_BASE_URL not set")
	}

	// Автоопределение доступной модели через /models
	availableModels := fetchAvailableModels(baseURL, apiKey)
	if len(availableModels) > 0 {
		model = selectBestVisionModel(availableModels, model)
		log.Printf("AI: выбрана модель %s (доступно %d моделей)", model, len(availableModels))
	} else {
		log.Printf("AI: список моделей не получен, используем fallback-кандидаты для %s", baseURL)
	}

	payloadImageURL := buildAIImagePayloadURL(imageURL)

	playerList := ""
	for _, p := range matchPlayers {
		playerList += fmt.Sprintf("- %s (team: %s, userID: %d, discordID: %s)\n", p.Username, p.Team, p.UserID, p.DiscordID)
	}

	prompt := fmt.Sprintf(`You are analyzing a Standoff 2 screenshot.

FIRST: determine if this image is a FINAL end-game scoreboard (the screen shown AFTER the match ends, with "ПОБЕДА"/"ПОРАЖЕНИЕ"/"СТАТИСТИКА" header, showing both teams' final K/D/A stats).

If the image is NOT a final scoreboard (e.g. it's a random photo, meme, mid-game TAB, lobby screenshot, or anything else), return EXACTLY:
{"error": "not_a_scoreboard"}

If it IS a valid final scoreboard, return the match data as JSON.

Known players in this match:
%s

Return this exact JSON structure:
{
  "score_ct": "number",
  "score_t": "number",
  "players": [
    {
      "username": "matched player username from known list",
      "discord_id": "their discord_id",
      "user_id": number,
      "team": "CT or T",
      "kills": number,
      "deaths": number,
      "assists": number
    }
  ]
}

Rules:
- Use ONLY players from the known list.
- Never assign one visible scoreboard row to two different players.
- Fuzzy-match nicknames: ignore clan tags, OLD prefixes, brackets, punctuation and harmless extra text.
- If a player is visible with a different but clearly matching nickname, still return the known player's exact user_id and discord_id.
- If a player's row is truly absent from the final scoreboard, do not invent stats.
- If a visible row is 0/0/0, return that row exactly as 0/0/0.
- Read ONLY K/D/A from the image itself.
- Do not guess unreadable numbers.`, playerList)

	reqBody := map[string]any{
		"model": model,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": prompt},
					{"type": "image_url", "image_url": map[string]string{"url": payloadImageURL}},
				},
			},
		},
		"max_tokens": 2000,
	}

	// response_format поддерживается не всеми провайдерами, добавляем только для известных
	lowerBase := strings.ToLower(baseURL)
	if strings.Contains(lowerBase, "openai.com") || strings.Contains(lowerBase, "openrouter") {
		reqBody["response_format"] = map[string]string{"type": "json_object"}
	}

	endpoint := baseURL + "/chat/completions"

	type authAttempt struct {
		Name    string
		Headers map[string]string
	}

	parseSuccess := func(respBody []byte) (*RecognitionResult, error) {
		var aiResp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(respBody, &aiResp); err != nil {
			return nil, fmt.Errorf("parse AI response: %v", err)
		}
		if len(aiResp.Choices) == 0 {
			return nil, fmt.Errorf("no AI response choices")
		}

		content := strings.TrimSpace(aiResp.Choices[0].Message.Content)
		if idx := strings.Index(content, "{"); idx >= 0 { content = content[idx:] }
		if idx := strings.LastIndex(content, "}"); idx >= 0 { content = content[:idx+1] }

		// Check if AI rejected the image as not a scoreboard
		if strings.Contains(content, "not_a_scoreboard") {
			return nil, fmt.Errorf("NOT_SCOREBOARD")
		}

		var parsed struct {
			ScoreCT json.Number `json:"score_ct"`
			ScoreT  json.Number `json:"score_t"`
			Players []struct {
				Username  string `json:"username"`
				DiscordID string `json:"discord_id"`
				UserID    int    `json:"user_id"`
				Team      string `json:"team"`
				Kills     int    `json:"kills"`
				Deaths    int    `json:"deaths"`
				Assists   int    `json:"assists"`
			} `json:"players"`
		}
		if err := json.Unmarshal([]byte(content), &parsed); err != nil {
			return nil, fmt.Errorf("parse AI JSON: %v (raw: %s)", err, content[:min(len(content), 300)])
		}

		result := &RecognitionResult{
			ScoreCT: parsed.ScoreCT.String(),
			ScoreT:  parsed.ScoreT.String(),
			Score:   parsed.ScoreCT.String() + ":" + parsed.ScoreT.String(),
		}
		for _, p := range parsed.Players {
			result.Players = append(result.Players, RecognizedPlayer{
				DiscordID: p.DiscordID,
				Username:  p.Username,
				UserID:    p.UserID,
				Team:      p.Team,
				Kills:     p.Kills,
				Deaths:    p.Deaths,
				Assists:   p.Assists,
			})
		}
		return result, nil
	}

	authMode := strings.ToLower(cleanEnvSecret(os.Getenv("AI_AUTH_MODE")))
	authAttempts := []authAttempt{{
		Name:    "bearer",
		Headers: map[string]string{"Authorization": "Bearer " + apiKey},
	}}
	if authMode == "" || authMode == "smart" || authMode == "auto" {
		authAttempts = append(authAttempts,
			authAttempt{Name: "raw-authorization", Headers: map[string]string{"Authorization": apiKey}},
			authAttempt{Name: "x-api-key", Headers: map[string]string{"x-api-key": apiKey}},
			authAttempt{Name: "api-key", Headers: map[string]string{"api-key": apiKey}},
		)
	} else if authMode == "raw" {
		authAttempts = []authAttempt{{Name: "raw-authorization", Headers: map[string]string{"Authorization": apiKey}}}
	} else if authMode == "x-api-key" {
		authAttempts = []authAttempt{{Name: "x-api-key", Headers: map[string]string{"x-api-key": apiKey}}}
	} else if authMode == "api-key" {
		authAttempts = []authAttempt{{Name: "api-key", Headers: map[string]string{"api-key": apiKey}}}
	}

	modelsToTry := buildModelCandidates(model, availableModels, baseURL)

	var finalErr error
	for _, currentModel := range modelsToTry {
		log.Printf("AI: пробуем модель %s", currentModel)
		reqBody["model"] = currentModel
		bodyBytes, _ := json.Marshal(reqBody)

		var lastErr error
		var bestStatus int
		var bestBody []byte
		var bestAttempt string

		for idx, attempt := range authAttempts {
			req, err := http.NewRequest("POST", endpoint, bytes.NewReader(bodyBytes))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			for header, value := range attempt.Headers {
				req.Header.Set(header, value)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				lastErr = err
				continue
			}

			respBody, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				lastErr = readErr
				continue
			}

			if resp.StatusCode == 200 {
				return parseSuccess(respBody)
			}

			if idx == 0 || bestBody == nil {
				bestStatus = resp.StatusCode
				bestBody = respBody
				bestAttempt = attempt.Name
			}
			lastErr = fmt.Errorf("attempt %s failed", attempt.Name)

			lowerBody := strings.ToLower(string(respBody))
			if strings.Contains(lowerBody, "invalid_api_key") || strings.Contains(lowerBody, "invalid api key") {
				continue
			}
			if strings.Contains(lowerBody, "missing authorization header") || strings.Contains(lowerBody, "use authorization: bearer") {
				break
			}
			if resp.StatusCode != 401 && resp.StatusCode != 403 {
				break
			}
		}

		if len(bestBody) > 0 {
			lowerBody := strings.ToLower(string(bestBody))
			if strings.Contains(lowerBody, "model_not_found") || strings.Contains(lowerBody, "unknown model") || strings.Contains(lowerBody, "does not exist") {
				finalErr = fmt.Errorf("AI API error %d (%s): %s", bestStatus, bestAttempt, string(bestBody[:min(len(bestBody), 400)]))
				continue
			}
			return nil, fmt.Errorf("AI API error %d (%s): %s", bestStatus, bestAttempt, string(bestBody[:min(len(bestBody), 400)]))
		}
		if lastErr != nil {
			finalErr = lastErr
			continue
		}
	}

	if finalErr != nil {
		return nil, finalErr
	}
	return nil, fmt.Errorf("AI API request failed")
}

func min(a, b int) int { if a < b { return a }; return b }
