package main

import (
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	_ "github.com/glebarez/go-sqlite"
)

var (
	db      *sql.DB
	session *discordgo.Session
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
	MatchID    int
	ChannelID  string
	MessageID  string
	TCapID     string // discord user id of T captain
	CTCapID    string // discord user id of CT captain
	TTeam      []PlayerInfo
	CTTeam     []PlayerInfo
	BannedMaps []string
	Turn       string // "T" or "CT"
	League     string
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

	err = session.Open()
	if err != nil {
		log.Fatal("Error opening connection:", err)
	}
	defer session.Close()

	fmt.Println("Bot is running. Press Ctrl+C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	<-sc
}

// ── Database ────────────────────────────────────────────────────────────

func initDB() {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./data"
	}
	os.MkdirAll(dbPath, 0755)

	var err error
	db, err = sql.Open("sqlite", dbPath+"/bot.db")
	if err != nil {
		log.Fatal(err)
	}

	db.Exec(`CREATE TABLE IF NOT EXISTS users (
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

	db.Exec(`CREATE TABLE IF NOT EXISTS matches (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		status TEXT DEFAULT 'picking',
		map_name TEXT DEFAULT '',
		score_t INTEGER DEFAULT 0,
		score_ct INTEGER DEFAULT 0,
		host_id TEXT DEFAULT '',
		category TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)

	db.Exec(`CREATE TABLE IF NOT EXISTS match_players (
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

	db.Exec(`CREATE TABLE IF NOT EXISTS map_stats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER,
		map_name TEXT,
		wins INTEGER DEFAULT 0,
		losses INTEGER DEFAULT 0,
		kills INTEGER DEFAULT 0,
		deaths INTEGER DEFAULT 0
	)`)

	// Upgrade existing databases safely
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN game_nick TEXT DEFAULT ''")
	_, _ = db.Exec("ALTER TABLE users ADD COLUMN game_id TEXT DEFAULT ''")
}

func getOrCreateUser(discordID, username string) (int, error) {
	var id int
	err := db.QueryRow("SELECT id FROM users WHERE discord_id = ?", discordID).Scan(&id)
	if err == sql.ErrNoRows {
		res, err := db.Exec("INSERT INTO users (discord_id, username) VALUES (?, ?)", discordID, username)
		if err != nil {
			return 0, err
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
	if v.ChannelID == "" {
		return
	}
	// If user joined a channel
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
	_ = db.QueryRow("SELECT COALESCE(NULLIF(game_nick, ''), username) FROM users WHERE discord_id = ?", v.UserID).Scan(&displayNick)
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
	// Check if already active in this channel
	if _, exists := activePickBans[channelID]; exists {
		return
	}

	// Create match in DB
	res, err := db.Exec("INSERT INTO matches (status, category) VALUES ('picking', ?)", league)
	if err != nil {
		log.Println("Error creating match:", err)
		return
	}
	matchID64, _ := res.LastInsertId()
	matchID := int(matchID64)

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
		db.Exec("INSERT INTO match_players (match_id, user_id, team, is_captain) VALUES (?, ?, 'T', ?)", matchID, p.UniqueID, isCap)
	}
	for _, p := range ctTeam {
		isCap := 0
		if p.IsCaptain {
			isCap = 1
		}
		db.Exec("INSERT INTO match_players (match_id, user_id, team, is_captain) VALUES (?, ?, 'CT', ?)", matchID, p.UniqueID, isCap)
	}

	state := &PickBanState{
		MatchID:    matchID,
		ChannelID:  channelID,
		TCapID:     tTeam[tCapIdx].DiscordID,
		CTCapID:    ctTeam[ctCapIdx].DiscordID,
		TTeam:      tTeam,
		CTTeam:     ctTeam,
		BannedMaps: []string{},
		Turn:       "T",
		League:     league,
	}
	activePickBans[channelID] = state

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
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "⏳ Полностью пересоздаю структуру сервера...",
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})

	guildID := i.GuildID
	minPlayers := 10
	if mp := os.Getenv("MIN_LOBBY_PLAYERS"); mp != "" {
		if n, err := strconv.Atoi(mp); err == nil && n > 0 {
			minPlayers = n
		}
	}

	// 1. Удалить все каналы
	channels, _ := s.GuildChannels(guildID)
	for _, ch := range channels {
		_, _ = s.ChannelDelete(ch.ID)
	}

	// 2. Удалить старые роли бота
	botRoleNames := map[string]bool{"No registred": true, "Registered": true, "Game Admin": true, "Ticket Admin": true}
	for _, lr := range LeagueRoles {
		botRoleNames[lr] = true
	}
	roles, _ := s.GuildRoles(guildID)
	for _, r := range roles {
		if botRoleNames[r.Name] {
			_ = s.GuildRoleDelete(guildID, r.ID)
		}
	}

	// 3. Создать роли
	noRegRole, _ := s.GuildRoleCreate(guildID, &discordgo.RoleParams{Name: "No registred"})
	gameAdminRole, _ := s.GuildRoleCreate(guildID, &discordgo.RoleParams{Name: "Game Admin"})
	ticketAdminRole, _ := s.GuildRoleCreate(guildID, &discordgo.RoleParams{Name: "Ticket Admin"})

	gameAdminRoleID := ""
	ticketAdminRoleID := ""
	noRegRoleID := ""
	if gameAdminRole != nil { gameAdminRoleID = gameAdminRole.ID }
	if ticketAdminRole != nil { ticketAdminRoleID = ticketAdminRole.ID }
	if noRegRole != nil { noRegRoleID = noRegRole.ID }

	// Роли лиг
	leagueRoleIDs := map[string]string{}
	for _, lr := range LeagueRoles {
		role, err := s.GuildRoleCreate(guildID, &discordgo.RoleParams{Name: lr})
		if err == nil && role != nil {
			leagueRoleIDs[lr] = role.ID
		}
	}

	privateAllow := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionReadMessageHistory | discordgo.PermissionVoiceConnect | discordgo.PermissionVoiceSpeak | discordgo.PermissionVoiceUseVAD)
	privateDeny := int64(discordgo.PermissionViewChannel)
	publicAllow := int64(discordgo.PermissionViewChannel | discordgo.PermissionSendMessages | discordgo.PermissionReadMessageHistory)

	// 4. Категория регистрации
	registrationCat, _ := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
		Name: "🪪 | REGISTRATION",
		Type: discordgo.ChannelTypeGuildCategory,
	})
	if registrationCat != nil {
		_ = s.ChannelPermissionSet(registrationCat.ID, guildID, discordgo.PermissionOverwriteTypeRole, publicAllow, 0)
		if noRegRoleID != "" {
			_ = s.ChannelPermissionSet(registrationCat.ID, noRegRoleID, discordgo.PermissionOverwriteTypeRole, publicAllow, 0)
		}
	}

	registrationCh, _ := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
		Name:     "🪪│регистрация",
		Type:     discordgo.ChannelTypeGuildText,
		ParentID: func() string { if registrationCat != nil { return registrationCat.ID }; return "" }(),
	})
	if registrationCh != nil {
		regEmbed := &discordgo.MessageEmbed{
			Title: "🪪 РЕГИСТРАЦИЯ",
			Description: "Добро пожаловать! Для доступа к лигам нужно зарегистрироваться.\n\n**Что нужно указать**\n› Игровой ник\n› Игровой ID\n\nИгровой ID будет использоваться для хоста лобби, если ты станешь хостом матча.",
			Color: 0x5865f2,
		}
		regComponents := []discordgo.MessageComponent{
			discordgo.ActionsRow{Components: []discordgo.MessageComponent{
				discordgo.Button{Label: "Регистрация", Style: discordgo.SuccessButton, CustomID: "open_register", Emoji: &discordgo.ComponentEmoji{Name: "🪪"}},
			}},
		}
		_, _ = s.ChannelMessageSendComplex(registrationCh.ID, &discordgo.MessageSend{Embeds: []*discordgo.MessageEmbed{regEmbed}, Components: regComponents})
	}

	// 5. Лиги и лобби — создаём последовательно, каждую категорию сразу ставим на нужную позицию
	type createdCat struct {
		id       string
		position int
	}
	var allCats []createdCat
	catPos := 1 // 0 = регистрация

	for _, league := range LeagueConfig {
		for _, lobbyNum := range league.Lobbies {
			catName := fmt.Sprintf("%s | %s", league.Emoji, league.Name)

			cat, err := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
				Name:     catName,
				Type:     discordgo.ChannelTypeGuildCategory,
				Position: catPos,
			})
			if err != nil || cat == nil {
				log.Printf("Error creating category %s lobby %d: %v", catName, lobbyNum, err)
				continue
			}

			// Права: скрыть от всех, показать роли лиги + админам
			_ = s.ChannelPermissionSet(cat.ID, guildID, discordgo.PermissionOverwriteTypeRole, 0, privateDeny)
			if rid, ok := leagueRoleIDs[league.RoleName]; ok {
				_ = s.ChannelPermissionSet(cat.ID, rid, discordgo.PermissionOverwriteTypeRole, privateAllow, 0)
			}
			if gameAdminRoleID != "" {
				_ = s.ChannelPermissionSet(cat.ID, gameAdminRoleID, discordgo.PermissionOverwriteTypeRole, privateAllow, 0)
			}
			if ticketAdminRoleID != "" {
				_ = s.ChannelPermissionSet(cat.ID, ticketAdminRoleID, discordgo.PermissionOverwriteTypeRole, privateAllow, 0)
			}

			// ranked + lobby внутри категории
			_, _ = s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
				Name: "ranked", Type: discordgo.ChannelTypeGuildText, ParentID: cat.ID, Position: 0,
			})
			_, _ = s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
				Name: fmt.Sprintf("Lobby %d", lobbyNum), Type: discordgo.ChannelTypeGuildVoice, ParentID: cat.ID, Position: 1, UserLimit: minPlayers,
			})

			allCats = append(allCats, createdCat{cat.ID, catPos})
			catPos++

			// Пауза чтобы Discord API не перемешал
			time.Sleep(500 * time.Millisecond)
		}
	}

	// 6. SEND RESULT'S
	resultsCatID := ""
	resultsCat, _ := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
		Name: "📍 | SEND RESULT'S", Type: discordgo.ChannelTypeGuildCategory, Position: catPos,
	})
	if resultsCat != nil {
		resultsCatID = resultsCat.ID
		_ = s.ChannelPermissionSet(resultsCatID, guildID, discordgo.PermissionOverwriteTypeRole, 0, privateDeny)
		for _, rid := range leagueRoleIDs {
			_ = s.ChannelPermissionSet(resultsCatID, rid, discordgo.PermissionOverwriteTypeRole, privateAllow, 0)
		}
		if gameAdminRoleID != "" {
			_ = s.ChannelPermissionSet(resultsCatID, gameAdminRoleID, discordgo.PermissionOverwriteTypeRole, privateAllow, 0)
		}
		if ticketAdminRoleID != "" {
			_ = s.ChannelPermissionSet(resultsCatID, ticketAdminRoleID, discordgo.PermissionOverwriteTypeRole, privateAllow, 0)
		}
	}

	sendResultsCh, _ := s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
		Name: "📌│отправить-результаты", Type: discordgo.ChannelTypeGuildText, ParentID: resultsCatID,
	})
	_, _ = s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
		Name: "📊│история-игр", Type: discordgo.ChannelTypeGuildText, ParentID: resultsCatID,
	})
	_, _ = s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
		Name: "📁│приоритет-игр", Type: discordgo.ChannelTypeGuildText, ParentID: resultsCatID,
	})
	_, _ = s.GuildChannelCreateComplex(guildID, discordgo.GuildChannelCreateData{
		Name: "📁│обычный-рег", Type: discordgo.ChannelTypeGuildText, ParentID: resultsCatID,
	})

	if sendResultsCh != nil {
		embed := &discordgo.MessageEmbed{
			Title:       "📌 ОТПРАВКА РЕЗУЛЬТАТА",
			Description: "Сыграли матч? Отправьте результат.\n\n**Как отправить**\n› Нажми **Отправить результат**\n› Укажи ID матча (номер из карточки)\n› Бот создаст ветку — скинь туда скриншот\n\n**Что дальше**\n› Заявка уходит модераторам матчей\n› Модератор вписывает счёт и статистику каждого игрока\n› Очки, рейтинг и статистика по картам обновляются автоматически\n› Отправить результат может любой участник матча",
			Color:       0xe91e63,
			Footer:      &discordgo.MessageEmbedFooter{Text: "ELITE FPL • результаты"},
		}
		_, _ = s.ChannelMessageSendComplex(sendResultsCh.ID, &discordgo.MessageSend{
			Embeds: []*discordgo.MessageEmbed{embed},
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{Components: []discordgo.MessageComponent{
					discordgo.Button{Label: "Отправить результат", Style: discordgo.DangerButton, CustomID: "send_result", Emoji: &discordgo.ComponentEmoji{Name: "📝"}},
				}},
			},
		})
	}

	// 6.5 Принудительно выставить позиции ВСЕХ категорий
	time.Sleep(1 * time.Second)

	reorder := []*discordgo.Channel{}
	if registrationCat != nil {
		reorder = append(reorder, &discordgo.Channel{ID: registrationCat.ID, Position: 0})
	}
	for _, c := range allCats {
		reorder = append(reorder, &discordgo.Channel{ID: c.id, Position: c.position})
	}
	if resultsCat != nil {
		reorder = append(reorder, &discordgo.Channel{ID: resultsCat.ID, Position: catPos})
	}
	if err := s.GuildChannelsReorder(guildID, reorder); err != nil {
		log.Println("Reorder error:", err)
	}

	// 7. Всем текущим участникам выдать роли
	defaultLeagueRoleID := leagueRoleIDs["Default League"]
	members, _ := s.GuildMembers(guildID, "", 1000)
	for _, m := range members {
		if m.User == nil || m.User.Bot {
			continue
		}
		uid, _ := getOrCreateUser(m.User.ID, m.User.Username)
		var isRegistered int
		_ = db.QueryRow("SELECT is_registered FROM users WHERE discord_id = ?", m.User.ID).Scan(&isRegistered)
		if isRegistered == 1 {
			if defaultLeagueRoleID != "" {
				_ = s.GuildMemberRoleAdd(guildID, m.User.ID, defaultLeagueRoleID)
			}
			var regNick string
			_ = db.QueryRow("SELECT COALESCE(NULLIF(game_nick, ''), username) FROM users WHERE discord_id = ?", m.User.ID).Scan(&regNick)
			guild, _ := s.Guild(guildID)
			if guild != nil && guild.OwnerID != m.User.ID {
				_ = s.GuildMemberNickname(guildID, m.User.ID, buildServerNick(uid, regNick))
			}
		} else {
			if noRegRoleID != "" {
				_ = s.GuildMemberRoleAdd(guildID, m.User.ID, noRegRoleID)
			}
		}
	}

	s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: strPtr("✅ Настройка завершена! После регистрации выдаётся Default League.")})
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
		_, _ = db.Exec("UPDATE users SET username = ?, game_nick = ?, game_id = ? WHERE discord_id = ?", nick, nick, gameID, userID)
	} else {
		_, _ = db.Exec("UPDATE users SET username = ?, game_nick = ? WHERE discord_id = ?", nick, nick, userID)
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

	err := db.QueryRow(`SELECT id, username, elo, games, wins, losses, kills, deaths, assists, mvps, joined_at 
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
	rows, err := db.Query("SELECT map_name, wins, losses, kills, deaths FROM map_stats WHERE user_id = ?", id)
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

		remaining := getRemainingMaps(state.BannedMaps)

		// If 1 map left — match starts
		if len(remaining) == 1 {
			finalMap := remaining[0]
			db.Exec("UPDATE matches SET map_name = ?, status = 'playing' WHERE id = ?", finalMap.Name, matchID)

			// Delete old pick/ban message
			delete(activePickBans, i.ChannelID)

			// Send match card
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseUpdateMessage,
				Data: &discordgo.InteractionResponseData{
					Content:    "",
					Embeds:     []*discordgo.MessageEmbed{},
					Components: []discordgo.MessageComponent{},
				},
			})

			// Find host (T captain)
			hostMention := fmt.Sprintf("<@%s>", state.TCapID)

			mentions := ""
			for _, p := range append(state.TTeam, state.CTTeam...) {
				mentions += fmt.Sprintf("<@%s> ", p.DiscordID)
			}

			embed := &discordgo.MessageEmbed{
				Title:       fmt.Sprintf("#%d · %s %s", matchID, finalMap.Emoji, finalMap.Name),
				Description: fmt.Sprintf("Игра идёт до **13 раундов**\n**Комнаты:** 🔒 Нет доступа · 🔒 Нет доступа — зайти может только свой состав\n*Ждём ссылку на лобби от хоста...*"),
				Color:       0xe91e63,
				Footer: &discordgo.MessageEmbedFooter{
					Text: fmt.Sprintf("ID матча для отправки результата: %d • %s", matchID, time.Now().Format("02.01.2006 15:04")),
				},
			}

			components := []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.Button{
							Label:    "Отправить ссылку на лобби",
							Style:    discordgo.SuccessButton,
							CustomID: fmt.Sprintf("lobby_link_%d", matchID),
							Emoji:    &discordgo.ComponentEmoji{Name: "🔗"},
						},
						discordgo.Button{
							Label:    "Получить ID хоста",
							Style:    discordgo.PrimaryButton,
							CustomID: fmt.Sprintf("hostid_%d_%s", matchID, state.TCapID),
							Emoji:    &discordgo.ComponentEmoji{Name: "🆔"},
						},
						discordgo.Button{
							Label:    "Игрок не зашёл (0/6)",
							Style:    discordgo.DangerButton,
							CustomID: fmt.Sprintf("noshow_%d", matchID),
							Emoji:    &discordgo.ComponentEmoji{Name: "🚫"},
						},
					},
				},
			}

			s.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
				Content:    mentions,
				Embeds:     []*discordgo.MessageEmbed{embed},
				Components: components,
			})

			// Host message
			s.ChannelMessageSend(i.ChannelID, fmt.Sprintf("👑 %s — ты хост матча #%d. Создай лобби и нажми **Отправить ссылку на лобби**.", hostMention, matchID))

			// Create T and CT voice channels in same category
			ch, _ := s.Channel(i.ChannelID)
			if ch != nil && ch.ParentID != "" {
				s.GuildChannelCreateComplex(i.GuildID, discordgo.GuildChannelCreateData{
					Name:      fmt.Sprintf("T │ Матч #%d", matchID),
					Type:      discordgo.ChannelTypeGuildVoice,
					ParentID:  ch.ParentID,
					UserLimit: 5,
				})
				s.GuildChannelCreateComplex(i.GuildID, discordgo.GuildChannelCreateData{
					Name:      fmt.Sprintf("CT │ Матч #%d", matchID),
					Type:      discordgo.ChannelTypeGuildVoice,
					ParentID:  ch.ParentID,
					UserLimit: 5,
				})
			}

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
		return
	}

	// Registration button
	if customID == "open_register" {
		var isRegistered int
		_ = db.QueryRow("SELECT is_registered FROM users WHERE discord_id = ?", i.Member.User.ID).Scan(&isRegistered)
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
		_ = db.QueryRow("SELECT game_id FROM users WHERE discord_id = ?", hostDiscordID).Scan(&hostGameID)
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

		_, err = db.Exec(
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
		err = db.QueryRow("SELECT status FROM matches WHERE id = ?", matchID).Scan(&status)
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
		_, _ = db.Exec("UPDATE matches SET status = 'reporting' WHERE id = ?", matchID)

		s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("✅ Ветка создана: <#%s>", thread.ID),
				Flags:   discordgo.MessageFlagsEphemeral,
			},
		})
	}
}
