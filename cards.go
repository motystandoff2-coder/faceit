package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	imagedraw "image/draw"
	"image/png"
	_ "image/jpeg"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

type cardFaces struct {
	Tiny         font.Face
	Small        font.Face
	Body         font.Face
	BodyBold     font.Face
	SubTitle     font.Face
	Title        font.Face
	Display      font.Face
	DisplayLarge font.Face
	Italic       font.Face
}

var (
	facesOnce sync.Once
	facesData *cardFaces
)

func loadCardFaces() *cardFaces {
	facesOnce.Do(func() {
		mustFace := func(ttf []byte, size float64) font.Face {
			parsed, err := opentype.Parse(ttf)
			if err != nil {
				panic(err)
			}
			face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
			if err != nil {
				panic(err)
			}
			return face
		}
		facesData = &cardFaces{
			Tiny:         mustFace(goregular.TTF, 14),
			Small:        mustFace(goregular.TTF, 18),
			Body:         mustFace(goregular.TTF, 24),
			BodyBold:     mustFace(gobold.TTF, 24),
			SubTitle:     mustFace(goregular.TTF, 34),
			Title:        mustFace(gobold.TTF, 46),
			Display:      mustFace(gobold.TTF, 64),
			DisplayLarge: mustFace(gobold.TTF, 86),
			Italic:       mustFace(goitalic.TTF, 28),
		}
	})
	return facesData
}

type profileMapCard struct {
	Name   string
	Wins   int
	Losses int
	Kills  int
	Deaths int
	KD     float64
	WR     int
	Games  int
}

type leagueStanding struct {
	ProjectID int
	Name      string
	AvatarURL string
	Elo       int
}

type profileCardData struct {
	ProjectID      int
	DisplayName    string
	GameID         string
	AvatarURL      string
	League         string
	Elo            int
	Games          int
	Wins           int
	Losses         int
	Kills          int
	Deaths         int
	Assists        int
	MVPs           int
	JoinedAt       time.Time
	PlayHours      int
	KD             float64
	Avg            float64
	WinRate        int
	Impact         float64
	Rank           int
	LevelTarget    int
	LevelRemaining int
	MapCards       []profileMapCard
	BestMap        profileMapCard
	RecentResults  []string
	RatingBars     []int
	Leaderboard    []leagueStanding
}

type matchCardRow struct {
	Name      string
	DiscordID string
	AvatarURL string
	Team      string
	Kills     int
	Deaths    int
	Assists   int
	Rating    float64
	KD        float64
}

type matchCardData struct {
	MatchID    int
	MapName    string
	Category   string
	ScoreCT    int
	ScoreT     int
	CreatedAt  time.Time
	CTPlayers  []matchCardRow
	TPlayers   []matchCardRow
	MVP        matchCardRow
	MVPRating  float64
	MVPKD      float64
	MVPAssists int
}

func rgba(r, g, b int) color.RGBA {
	return color.RGBA{uint8(r), uint8(g), uint8(b), 255}
}

func rgbaA(r, g, b, a int) color.RGBA {
	return color.RGBA{uint8(r), uint8(g), uint8(b), uint8(a)}
}

func clampFloat(v, minV, maxV float64) float64 {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func interpolateColor(a, b color.RGBA, t float64) color.RGBA {
	t = clampFloat(t, 0, 1)
	return color.RGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		A: uint8(float64(a.A) + (float64(b.A)-float64(a.A))*t),
	}
}

func inRoundedRect(px, py, x, y, w, h, r int) bool {
	if px < x || py < y || px >= x+w || py >= y+h {
		return false
	}
	if r <= 0 {
		return true
	}
	left := x + r
	right := x + w - r - 1
	top := y + r
	bottom := y + h - r - 1
	if px >= left && px <= right {
		return true
	}
	if py >= top && py <= bottom {
		return true
	}
	corners := [][2]int{{left, top}, {right, top}, {left, bottom}, {right, bottom}}
	for _, c := range corners {
		dx := px - c[0]
		dy := py - c[1]
		if dx*dx+dy*dy <= r*r {
			return true
		}
	}
	return false
}

func fillRoundedRect(img *image.RGBA, x, y, w, h, r int, c color.RGBA) {
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			if inRoundedRect(px, py, x, y, w, h, r) {
				img.SetRGBA(px, py, c)
			}
		}
	}
}

func fillGradient(img *image.RGBA, x, y, w, h int, left, right color.RGBA, vertical bool) {
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			t := 0.0
			if vertical {
				if h > 1 {
					t = float64(py-y) / float64(h-1)
				}
			} else {
				if w > 1 {
					t = float64(px-x) / float64(w-1)
				}
			}
			img.SetRGBA(px, py, interpolateColor(left, right, t))
		}
	}
}

func fillRoundedGradientRect(img *image.RGBA, x, y, w, h, r int, left, right color.RGBA, vertical bool) {
	for py := y; py < y+h; py++ {
		for px := x; px < x+w; px++ {
			if !inRoundedRect(px, py, x, y, w, h, r) {
				continue
			}
			t := 0.0
			if vertical {
				if h > 1 {
					t = float64(py-y) / float64(h-1)
				}
			} else {
				if w > 1 {
					t = float64(px-x) / float64(w-1)
				}
			}
			img.SetRGBA(px, py, interpolateColor(left, right, t))
		}
	}
}

func drawText(img *image.RGBA, face font.Face, x, y int, c color.Color, text string) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x, y)}
	d.DrawString(text)
}

func textWidth(face font.Face, text string) int {
	return font.MeasureString(face, text).Ceil()
}

func trimText(face font.Face, value string, maxWidth int) string {
	if textWidth(face, value) <= maxWidth {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 {
		candidate := string(runes) + "…"
		if textWidth(face, candidate) <= maxWidth {
			return candidate
		}
		runes = runes[:len(runes)-1]
	}
	return value
}

func drawRightText(img *image.RGBA, face font.Face, right, y int, c color.Color, text string) {
	drawText(img, face, right-textWidth(face, text), y, c, text)
}

func drawCenteredText(img *image.RGBA, face font.Face, centerX, y int, c color.Color, text string) {
	drawText(img, face, centerX-textWidth(face, text)/2, y, c, text)
}

func drawProgressRing(img *image.RGBA, cx, cy, radius, thickness int, progress float64, bg, fg color.RGBA) {
	progress = clampFloat(progress, 0, 1)
	inner := float64(radius - thickness)
	outer := float64(radius)
	for py := cy - radius; py <= cy+radius; py++ {
		for px := cx - radius; px <= cx+radius; px++ {
			dx := float64(px - cx)
			dy := float64(py - cy)
			dist := math.Hypot(dx, dy)
			if dist < inner || dist > outer {
				continue
			}
			angle := math.Atan2(dy, dx) + math.Pi/2
			if angle < 0 {
				angle += 2 * math.Pi
			}
			if angle <= progress*2*math.Pi {
				img.SetRGBA(px, py, fg)
			} else {
				img.SetRGBA(px, py, bg)
			}
		}
	}
}

func drawHorizontalBar(img *image.RGBA, x, y, w, h int, progress float64, bg, fg color.RGBA) {
	fillRoundedRect(img, x, y, w, h, h/2, bg)
	fillRoundedRect(img, x, y, int(float64(w)*clampFloat(progress, 0, 1)), h, h/2, fg)
}

func decodeRemoteImage(url string) (image.Image, error) {
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("empty image url")
	}
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %s", resp.Status)
	}
	img, _, err := image.Decode(resp.Body)
	return img, err
}

func drawRoundedImageFromURL(dst *image.RGBA, url string, x, y, w, h, r int, fallback color.RGBA) {
	fillRoundedRect(dst, x, y, w, h, r, fallback)
	src, err := decodeRemoteImage(url)
	if err != nil {
		return
	}
	tmp := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(tmp, tmp.Bounds(), src, src.Bounds(), imagedraw.Over, nil)
	for py := 0; py < h; py++ {
		for px := 0; px < w; px++ {
			if inRoundedRect(x+px, y+py, x, y, w, h, r) {
				dst.Set(x+px, y+py, tmp.At(px, py))
			}
		}
	}
}

func parseJoinedAt(raw string) time.Time {
	layouts := []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05-07:00", "2006-01-02"}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Now()
}

func calculatePlayerRating(kills, deaths, assists int) float64 {
	// HLTV-style simplified rating
	// Kills-per-round component (assume ~24 rounds average)
	rounds := float64(maxInt(1, kills+deaths)) * 0.5
	if rounds < 10 {
		rounds = 10
	}
	kpr := float64(kills) / rounds
	dpr := float64(deaths) / rounds
	apr := float64(assists) / rounds

	rating := 0.0073*float64(kills) - 0.00458*float64(deaths) + 0.0032*float64(assists) + 0.3591
	if rating < 0.20 {
		// Alternative: impact-based
		impact := (kpr*0.75 + apr*0.15) / clampFloat(dpr*0.6+0.15, 0.15, 2.0)
		rating = 0.25 + impact*0.55
	}
	return math.Round(clampFloat(rating, 0.20, 2.50)*100) / 100
}

func findGuildLeagueRoleName(s *discordgo.Session, guildID, discordID string) string {
	member, err := s.GuildMember(guildID, discordID)
	if err != nil || member == nil {
		return "Default League"
	}
	roles, err := s.GuildRoles(guildID)
	if err != nil {
		return "Default League"
	}
	roleByID := map[string]string{}
	for _, role := range roles {
		roleByID[role.ID] = role.Name
	}
	for _, candidate := range []string{"Pro League", "Division League", "Prospect League", "Default League"} {
		for _, rid := range member.Roles {
			if roleByID[rid] == candidate {
				return candidate
			}
		}
	}
	return "Default League"
}

func normalizeLeagueOption(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pro", "pro league":
		return "Pro League"
	case "division", "division league":
		return "Division League"
	case "prospect", "prospect league":
		return "Prospect League"
	case "default", "default league":
		return "Default League"
	default:
		return strings.TrimSpace(value)
	}
}

func buildLeagueLeaderboard(s *discordgo.Session, guildID, leagueRoleName, targetDiscordID string) ([]leagueStanding, int) {
	roles, err := s.GuildRoles(guildID)
	if err != nil {
		return nil, 0
	}
	roleID := ""
	for _, role := range roles {
		if role.Name == leagueRoleName {
			roleID = role.ID
			break
		}
	}
	if roleID == "" {
		return nil, 0
	}
	members, err := s.GuildMembers(guildID, "", 1000)
	if err != nil {
		return nil, 0
	}
	var standings []leagueStanding
	for _, member := range members {
		if member.User == nil || member.User.Bot {
			continue
		}
		hasRole := false
		for _, rid := range member.Roles {
			if rid == roleID {
				hasRole = true
				break
			}
		}
		if !hasRole {
			continue
		}
		var projectID, elo int
		var name string
		err := queryRowDB("SELECT id, COALESCE(NULLIF(game_nick,''), username), elo FROM users WHERE discord_id = ?", member.User.ID).Scan(&projectID, &name, &elo)
		if err != nil {
			continue
		}
		standings = append(standings, leagueStanding{ProjectID: projectID, Name: name, AvatarURL: member.User.AvatarURL("128"), Elo: elo})
	}
	sort.SliceStable(standings, func(i, j int) bool { return standings[i].Elo > standings[j].Elo })
	rank := 0
	for idx, standing := range standings {
		if standing.ProjectID == 0 {
			continue
		}
		var discordID string
		_ = queryRowDB("SELECT discord_id FROM users WHERE id = ?", standing.ProjectID).Scan(&discordID)
		if discordID == targetDiscordID {
			rank = idx + 1
			break
		}
	}
	if len(standings) > 3 {
		standings = standings[:3]
	}
	return standings, rank
}

func recentMatchResults(userID int) []string {
	rows, err := queryDB(`SELECT mp.team, m.score_ct, m.score_t
		FROM match_players mp
		JOIN matches m ON m.id = mp.match_id
		WHERE mp.user_id = ? AND m.status = 'finished'
		ORDER BY m.id DESC
		LIMIT 21`, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var team string
		var scoreCT, scoreT int
		if err := rows.Scan(&team, &scoreCT, &scoreT); err != nil {
			continue
		}
		won := (team == "CT" && scoreCT > scoreT) || (team == "T" && scoreT > scoreCT)
		if won {
			result = append(result, "W")
		} else {
			result = append(result, "L")
		}
	}
	return result
}

func ratingBars(currentElo int, results []string) []int {
	if len(results) == 0 {
		return []int{currentElo}
	}
	values := []int{currentElo}
	cur := currentElo
	for _, result := range results {
		if result == "W" {
			cur -= 20
		} else {
			cur += 20
		}
		values = append(values, cur)
	}
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
	if len(values) > 16 {
		values = values[len(values)-16:]
	}
	return values
}

func profileMaps(userID int) ([]profileMapCard, profileMapCard) {
	cards := []profileMapCard{}
	best := profileMapCard{Name: "No data"}
	rows, err := queryDB("SELECT map_name, wins, losses, kills, deaths FROM map_stats WHERE user_id = ?", userID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var card profileMapCard
			if err := rows.Scan(&card.Name, &card.Wins, &card.Losses, &card.Kills, &card.Deaths); err != nil {
				continue
			}
			card.Games = card.Wins + card.Losses
			card.KD = math.Round((float64(card.Kills)/float64(maxInt(1, card.Deaths)))*100) / 100
			if card.Games > 0 {
				card.WR = card.Wins * 100 / card.Games
			}
			cards = append(cards, card)
		}
	}
	if len(cards) == 0 {
		for _, m := range Maps {
			cards = append(cards, profileMapCard{Name: m.Name})
		}
	}
	sort.SliceStable(cards, func(i, j int) bool {
		if cards[i].Games == cards[j].Games {
			return cards[i].WR > cards[j].WR
		}
		return cards[i].Games > cards[j].Games
	})
	if len(cards) > 0 {
		best = cards[0]
		for _, card := range cards {
			if card.Games > 0 && (card.WR > best.WR || best.Games == 0) {
				best = card
			}
		}
	}
	return cards, best
}

func buildProfileCardData(s *discordgo.Session, guildID string, targetUser *discordgo.User, preferredLeague string) (*profileCardData, error) {
	var data profileCardData
	var gameNick, joinedRaw string
	err := queryRowDB(`SELECT id, COALESCE(NULLIF(game_nick,''), username), COALESCE(NULLIF(game_id,''), ''), elo, games, wins, losses, kills, deaths, assists, mvps, joined_at
		FROM users WHERE discord_id = ?`, targetUser.ID).Scan(&data.ProjectID, &gameNick, &data.GameID, &data.Elo, &data.Games, &data.Wins, &data.Losses, &data.Kills, &data.Deaths, &data.Assists, &data.MVPs, &joinedRaw)
	if err != nil {
		return nil, err
	}
	data.DisplayName = gameNick
	data.AvatarURL = targetUser.AvatarURL("256")
	data.JoinedAt = parseJoinedAt(joinedRaw)
	data.PlayHours = int(math.Round(float64(data.Games*38) / 60.0))
	data.KD = math.Round((float64(data.Kills)/float64(maxInt(1, data.Deaths)))*100) / 100
	if data.Games > 0 {
		data.Avg = math.Round((float64(data.Kills)/float64(data.Games))*100) / 100
		data.WinRate = data.Wins * 100 / data.Games
	}
	data.Impact = math.Round((((float64(data.Kills)+0.35*float64(data.Assists))/float64(maxInt(1, data.Games*12)))+0.65)*100) / 100
	data.LevelTarget = ((data.Elo/100)+1)*100 + 100
	if data.LevelTarget < 1100 {
		data.LevelTarget = 1100
	}
	data.LevelRemaining = maxInt(0, data.LevelTarget-data.Elo)
	league := normalizeLeagueOption(preferredLeague)
	if league == "" {
		league = findGuildLeagueRoleName(s, guildID, targetUser.ID)
	}
	data.League = league
	data.Leaderboard, data.Rank = buildLeagueLeaderboard(s, guildID, league, targetUser.ID)
	data.RecentResults = recentMatchResults(data.ProjectID)
	data.RatingBars = ratingBars(data.Elo, data.RecentResults)
	data.MapCards, data.BestMap = profileMaps(data.ProjectID)
	if data.DisplayName == "" {
		data.DisplayName = targetUser.Username
	}
	if data.GameID == "" {
		data.GameID = targetUser.ID
	}
	return &data, nil
}

func findMemberAvatarURL(s *discordgo.Session, discordID string) string {
	user, err := s.User(discordID)
	if err != nil || user == nil {
		return ""
	}
	return user.AvatarURL("128")
}

func buildMatchCardData(s *discordgo.Session, matchID int) (*matchCardData, error) {
	var data matchCardData
	var createdRaw string
	if err := queryRowDB("SELECT id, COALESCE(map_name,''), COALESCE(category,''), score_ct, score_t, COALESCE(created_at, CURRENT_TIMESTAMP) FROM matches WHERE id = ?", matchID).Scan(&data.MatchID, &data.MapName, &data.Category, &data.ScoreCT, &data.ScoreT, &createdRaw); err != nil {
		return nil, err
	}
	data.CreatedAt = parseJoinedAt(createdRaw)
	rows, err := queryDB(`SELECT mp.team, mp.kills, mp.deaths, mp.assists, mp.rating,
		COALESCE(NULLIF(u.game_nick,''), u.username), u.discord_id
		FROM match_players mp
		JOIN users u ON u.id = mp.user_id
		WHERE mp.match_id = ?
		ORDER BY mp.team, mp.rating DESC, mp.kills DESC, mp.id ASC`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row matchCardRow
		if err := rows.Scan(&row.Team, &row.Kills, &row.Deaths, &row.Assists, &row.Rating, &row.Name, &row.DiscordID); err != nil {
			continue
		}
		row.KD = math.Round((float64(row.Kills)/float64(maxInt(1, row.Deaths)))*100) / 100
		if row.Rating <= 0 {
			row.Rating = calculatePlayerRating(row.Kills, row.Deaths, row.Assists)
		}
		row.AvatarURL = findMemberAvatarURL(s, row.DiscordID)
		if row.Team == "CT" {
			data.CTPlayers = append(data.CTPlayers, row)
		} else {
			data.TPlayers = append(data.TPlayers, row)
		}
		if row.Rating > data.MVPRating {
			data.MVPRating = row.Rating
			data.MVPKD = row.KD
			data.MVPAssists = row.Assists
			data.MVP = row
		}
	}
	return &data, nil
}

func renderMatchCardPNG(s *discordgo.Session, matchID int) ([]byte, error) {
	data, err := buildMatchCardData(s, matchID)
	if err != nil {
		return nil, err
	}
	faces := loadCardFaces()
	w, h := 1600, 900
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillGradient(img, 0, 0, w, h, rgba(38, 8, 52), rgba(17, 62, 8), false)
	fillRoundedRect(img, 28, 24, w-56, h-48, 34, rgbaA(20, 17, 25, 235))
	drawText(img, faces.DisplayLarge, 58, 98, rgba(148, 133, 160), fmt.Sprintf("#%d", data.MatchID))
	drawRightText(img, faces.Title, w-52, 74, rgba(247, 247, 247), "ELITE FPL")

	fillRoundedGradientRect(img, 46, 132, w-92, 92, 26, rgba(116, 42, 140), rgba(31, 108, 19), false)
	fillRoundedRect(img, 58, 144, 74, 68, 20, rgba(40, 35, 48))
	fillRoundedRect(img, w-132, 144, 74, 68, 20, rgba(40, 35, 48))
	drawText(img, faces.SubTitle, 150, 190, rgba(235, 223, 242), "COUNTER-TERRORISTS")
	drawRightText(img, faces.SubTitle, w-150, 190, rgba(219, 242, 219), "TERRORISTS")
	drawCenteredText(img, faces.BodyBold, w/2, 176, rgba(150, 150, 150), "SCORE")
	drawCenteredText(img, faces.Display, w/2, 214, rgba(255, 255, 255), fmt.Sprintf("%d:%d", data.ScoreCT, data.ScoreT))

	leftX := 48
	rightX := 815
	tableY := 266
	rowH := 78
	playerW := 390
	statW := 64
	gap := 8
	labelColor := rgba(239, 239, 239)
	drawText(img, faces.Body, leftX, tableY, labelColor, "PLAYER")
	cols := []string{"K", "D", "K/D", "A", "RATING"}
	for idx, label := range cols {
		x := leftX + playerW + gap + idx*(statW+gap)
		drawCenteredText(img, faces.Body, x+statW/2, tableY, labelColor, label)
	}
	drawRightText(img, faces.Body, w-48, tableY, labelColor, "PLAYER")
	for idx, label := range cols {
		x := rightX + idx*(statW+gap)
		drawCenteredText(img, faces.Body, x+statW/2, tableY, labelColor, label)
	}

	drawMatchRows := func(rows []matchCardRow, isLeft bool) {
		for idx := 0; idx < 5; idx++ {
			var row matchCardRow
			if idx < len(rows) {
				row = rows[idx]
			}
			y := tableY + 22 + idx*rowH
			if isLeft {
				fillRoundedRect(img, leftX, y, playerW, 64, 18, rgba(47, 44, 49))
				for col := 0; col < 5; col++ {
					boxX := leftX + playerW + gap + col*(statW+gap)
					shade := rgba(49, 45, 53)
					if col == 4 {
						shade = rgba(72, 23, 116)
					}
					fillRoundedRect(img, boxX, y, statW, 64, 14, shade)
				}
				if row.Name != "" {
					nameColor := rgba(240, 240, 240)
					if idx == 0 {
						nameColor = rgba(74, 230, 156)
					}
					drawText(img, faces.SubTitle, leftX+18, y+42, nameColor, trimText(faces.SubTitle, row.Name, playerW-30))
					vals := []string{fmt.Sprintf("%d", row.Kills), fmt.Sprintf("%d", row.Deaths), fmt.Sprintf("%.2f", row.KD), fmt.Sprintf("%d", row.Assists), fmt.Sprintf("%.2f", row.Rating)}
					for col, val := range vals {
						boxX := leftX + playerW + gap + col*(statW+gap)
						drawCenteredText(img, faces.SubTitle, boxX+statW/2, y+44, rgba(244, 244, 244), val)
					}
				}
			} else {
				for col := 0; col < 5; col++ {
					boxX := rightX + col*(statW+gap)
					shade := rgba(47, 52, 46)
					if col == 0 {
						shade = rgba(18, 90, 20)
					}
					fillRoundedRect(img, boxX, y, statW, 64, 14, shade)
				}
				nameX := rightX + 5*(statW+gap)
				fillRoundedRect(img, nameX, y, playerW, 64, 18, rgba(47, 44, 49))
				if row.Name != "" {
					vals := []string{fmt.Sprintf("%d", row.Kills), fmt.Sprintf("%d", row.Deaths), fmt.Sprintf("%.2f", row.KD), fmt.Sprintf("%d", row.Assists), fmt.Sprintf("%.2f", row.Rating)}
					for col, val := range vals {
						boxX := rightX + col*(statW+gap)
						drawCenteredText(img, faces.SubTitle, boxX+statW/2, y+44, rgba(244, 244, 244), val)
					}
					drawRightText(img, faces.SubTitle, nameX+playerW-18, y+42, rgba(244, 244, 244), trimText(faces.SubTitle, row.Name, playerW-30))
				}
			}
		}
	}
	drawMatchRows(data.CTPlayers, true)
	drawMatchRows(data.TPlayers, false)

	drawText(img, faces.Body, 58, 708, rgba(226, 226, 226), "MAP INFO")
	fillRoundedRect(img, 58, 728, 730, 150, 24, rgba(39, 35, 46))
	drawCenteredText(img, faces.Italic, 423, 790, rgba(192, 192, 192), "Picked map")
	drawCenteredText(img, faces.Display, 423, 842, rgba(255, 255, 255), data.MapName)

	drawText(img, faces.Body, 850, 708, rgba(226, 226, 226), "PLAYER INFO")
	drawText(img, faces.Body, 1282, 708, rgba(226, 226, 226), "RATING")
	drawText(img, faces.Body, 1406, 708, rgba(226, 226, 226), "K/D")
	drawText(img, faces.Body, 1500, 708, rgba(226, 226, 226), "A")
	fillRoundedRect(img, 850, 728, 702, 150, 24, rgba(39, 35, 46))
	drawRoundedImageFromURL(img, data.MVP.AvatarURL, 864, 742, 140, 120, 18, rgba(52, 48, 55))
	drawText(img, faces.Italic, 1036, 794, rgba(198, 198, 198), "Game MVP")
	drawText(img, faces.Display, 1036, 844, rgba(255, 255, 255), trimText(faces.Display, data.MVP.Name, 320))
	fillRoundedRect(img, 1260, 742, 96, 120, 20, rgba(28, 24, 34))
	fillRoundedRect(img, 1378, 742, 96, 120, 20, rgba(28, 24, 34))
	fillRoundedRect(img, 1496, 742, 96, 120, 20, rgba(28, 24, 34))
	drawCenteredText(img, faces.Display, 1308, 822, rgba(255, 255, 255), fmt.Sprintf("%.2f", data.MVPRating))
	drawCenteredText(img, faces.Display, 1426, 822, rgba(255, 255, 255), fmt.Sprintf("%.2f", data.MVPKD))
	drawCenteredText(img, faces.Display, 1544, 822, rgba(255, 255, 255), fmt.Sprintf("%d", data.MVPAssists))

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func renderProfileCardPNG(s *discordgo.Session, guildID string, targetUser *discordgo.User, preferredLeague string) ([]byte, error) {
	data, err := buildProfileCardData(s, guildID, targetUser, preferredLeague)
	if err != nil {
		return nil, err
	}
	faces := loadCardFaces()
	w, h := 1600, 1800
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillGradient(img, 0, 0, w, h, rgba(54, 12, 74), rgba(17, 79, 11), false)

	banner := image.Rect(70, 40, w-70, 220)
	fillRoundedRect(img, banner.Min.X, banner.Min.Y, banner.Dx(), banner.Dy(), 38, rgbaA(35, 33, 37, 235))
	drawRoundedImageFromURL(img, data.AvatarURL, 106, 78, 200, 200, 28, rgba(70, 70, 75))
	drawText(img, faces.Display, 340, 174, rgba(245, 245, 245), trimText(faces.Display, data.DisplayName, 420))
	drawText(img, faces.SubTitle, 340, 214, rgba(155, 155, 166), fmt.Sprintf("#%d", data.ProjectID))
	drawText(img, faces.SubTitle, 480, 214, rgba(155, 155, 166), fmt.Sprintf("ID: %s", data.GameID))
	for i := 0; i < 4; i++ {
		fillRoundedRect(img, 1240+i*64, 120, 56, 56, 12, rgba(54, 52, 57))
	}

	leftPanelX, leftPanelY := 70, 330
	leftPanelW, leftPanelH := 960, 690
	fillRoundedRect(img, leftPanelX, leftPanelY, leftPanelW, leftPanelH, 34, rgbaA(35, 33, 37, 235))
	drawText(img, faces.Title, leftPanelX+110, leftPanelY+66, rgba(241, 241, 241), "Statistic")
	fillRoundedRect(img, leftPanelX+50, leftPanelY+96, 455, 185, 24, rgba(52, 50, 55))
	drawProgressRing(img, leftPanelX+118, leftPanelY+188, 46, 8, float64(data.Elo)/float64(maxInt(1000, data.LevelTarget)), rgba(93, 92, 96), rgba(195, 71, 255))
	drawText(img, faces.Display, leftPanelX+175, leftPanelY+188, rgba(243, 243, 243), fmt.Sprintf("%d", data.Elo))
	drawRightText(img, faces.SubTitle, leftPanelX+485, leftPanelY+188, rgba(241, 241, 241), fmt.Sprintf("#%d", maxInt(1, data.Rank)))
	drawText(img, faces.SubTitle, leftPanelX+175, leftPanelY+232, rgba(154, 154, 162), fmt.Sprintf("%d / %d", data.Elo, data.LevelTarget))
	drawText(img, faces.SubTitle, leftPanelX+380, leftPanelY+232, rgba(190, 82, 255), fmt.Sprintf("%d до уровня", data.LevelRemaining))
	drawHorizontalBar(img, leftPanelX+175, leftPanelY+246, 205, 12, float64(data.Elo)/float64(maxInt(1000, data.LevelTarget)), rgba(72, 81, 91), rgba(194, 72, 255))

	fillRoundedRect(img, leftPanelX+50, leftPanelY+300, 455, 245, 24, rgba(52, 50, 55))
	drawProgressRing(img, leftPanelX+118, leftPanelY+392, 52, 9, clampFloat(data.KD/2.0, 0, 1), rgba(93, 92, 96), rgba(55, 223, 146))
	drawText(img, faces.Display, leftPanelX+165, leftPanelY+382, rgba(57, 224, 149), fmt.Sprintf("%.2f", data.KD))
	drawText(img, faces.SubTitle, leftPanelX+165, leftPanelY+420, rgba(245, 245, 245), "Рейтинг")
	drawText(img, faces.SubTitle, leftPanelX+165, leftPanelY+460, rgba(155, 155, 166), fmt.Sprintf("K/D %.2f   AVG %.0f", data.KD, data.Avg))
	for idx, cell := range []struct {
		Label string
		Value string
		Color color.RGBA
	}{
		{"Убийства", fmt.Sprintf("%d", data.Kills), rgba(245, 245, 245)},
		{"Смерти", fmt.Sprintf("%d", data.Deaths), rgba(245, 245, 245)},
		{"Ассисты", fmt.Sprintf("%d", data.Assists), rgba(245, 245, 245)},
		{"AVG", fmt.Sprintf("%.0f", data.Avg), rgba(245, 245, 245)},
		{"Победы", fmt.Sprintf("%d", data.Wins), rgba(55, 223, 146)},
		{"Поражения", fmt.Sprintf("%d", data.Losses), rgba(255, 88, 116)},
	} {
		cx := leftPanelX + 70 + (idx%3)*145
		cy := leftPanelY + 488 + (idx/3)*82
		fillRoundedRect(img, cx, cy, 132, 70, 12, rgba(62, 60, 65))
		drawText(img, faces.Small, cx+12, cy+24, rgba(187, 187, 192), cell.Label)
		drawText(img, faces.SubTitle, cx+12, cy+60, cell.Color, cell.Value)
	}

	fillRoundedRect(img, leftPanelX+525, leftPanelY+96, 405, 350, 24, rgba(52, 50, 55))
	drawText(img, faces.BodyBold, leftPanelX+545, leftPanelY+150, rgba(245, 245, 245), "История рейтинга")
	currentBarX := leftPanelX + 548
	if len(data.RatingBars) > 0 {
		minBar, maxBar := data.RatingBars[0], data.RatingBars[0]
		for _, v := range data.RatingBars {
			if v < minBar {
				minBar = v
			}
			if v > maxBar {
				maxBar = v
			}
		}
		drawText(img, faces.Display, leftPanelX+545, leftPanelY+212, rgba(245, 245, 245), fmt.Sprintf("%d", data.Elo))
		if len(data.RatingBars) > 1 {
			delta := data.RatingBars[len(data.RatingBars)-1] - data.RatingBars[0]
			colorDelta := rgba(255, 88, 116)
			if delta >= 0 {
				colorDelta = rgba(55, 223, 146)
			}
			drawText(img, faces.SubTitle, leftPanelX+675, leftPanelY+212, colorDelta, fmt.Sprintf("%+d", delta))
		}
		barBaseY := leftPanelY + 360
		for _, v := range data.RatingBars {
			norm := 0.1
			if maxBar > minBar {
				norm = float64(v-minBar) / float64(maxBar-minBar)
			}
			barH := int(24 + norm*145)
			fillRoundedRect(img, currentBarX, barBaseY-barH, 14, barH, 7, rgba(55, 223, 146))
			currentBarX += 21
		}
	}
	drawText(img, faces.Body, leftPanelX+545, leftPanelY+412, rgba(155, 155, 166), "27 сен.")
	drawRightText(img, faces.Body, leftPanelX+910, leftPanelY+412, rgba(155, 155, 166), time.Now().Format("02 янв."))

	fillRoundedRect(img, leftPanelX+525, leftPanelY+466, 405, 100, 24, rgba(52, 50, 55))
	drawProgressRing(img, leftPanelX+610, leftPanelY+520, 40, 8, clampFloat(data.Impact/2.0, 0, 1), rgba(93, 92, 96), rgba(55, 223, 146))
	drawText(img, faces.Display, leftPanelX+665, leftPanelY+514, rgba(55, 223, 146), fmt.Sprintf("%.2f", data.Impact))
	drawText(img, faces.SubTitle, leftPanelX+665, leftPanelY+548, rgba(245, 245, 245), "Impact")
	drawText(img, faces.SubTitle, leftPanelX+665, leftPanelY+580, rgba(155, 155, 166), "Влияние на раунд")

	fillRoundedRect(img, 1060, 330, 470, 210, 32, rgbaA(30, 36, 25, 235))
	statsTop := []struct{ Label, Value string }{
		{"Playtime", fmt.Sprintf("%dh", data.PlayHours)},
		{"Join Date", data.JoinedAt.Format("02.01.2006")},
		{"Games", fmt.Sprintf("%d", data.Games)},
		{"MVP", fmt.Sprintf("%d", data.MVPs)},
	}
	for idx, item := range statsTop {
		x := 1160 + (idx%2)*200
		y := 398 + (idx/2)*92
		drawText(img, faces.SubTitle, x, y, rgba(196, 196, 196), item.Label)
		drawText(img, faces.Display, x, y+44, rgba(245, 245, 245), item.Value)
	}

	fillRoundedRect(img, 1060, 570, 470, 530, 32, rgbaA(35, 33, 37, 235))
	drawText(img, faces.SubTitle, 1098, 630, rgba(196, 196, 196), "League")
	drawText(img, faces.Title, 1098, 692, rgba(245, 245, 245), data.League)
	drawText(img, faces.SubTitle, 1098, 740, rgba(196, 196, 196), "Places")
	for idx, item := range data.Leaderboard {
		y := 812 + idx*100
		drawText(img, faces.SubTitle, 1100, y, rgba(245, 245, 245), fmt.Sprintf("#%d", idx+1))
		drawRoundedImageFromURL(img, item.AvatarURL, 1158, y-52, 64, 64, 14, rgba(72, 70, 75))
		drawText(img, faces.SubTitle, 1240, y, rgba(245, 245, 245), trimText(faces.SubTitle, item.Name, 220))
	}
	fillRoundedRect(img, 1094, 994, 396, 2, 0, rgba(110, 110, 110))
	drawText(img, faces.Display, 1098, 1072, rgba(245, 245, 245), fmt.Sprintf("#%d", maxInt(1, data.Rank)))
	drawRoundedImageFromURL(img, data.AvatarURL, 1158, 1016, 64, 64, 14, rgba(72, 70, 75))
	drawText(img, faces.SubTitle, 1240, 1066, rgba(55, 223, 146), trimText(faces.SubTitle, data.DisplayName, 220))

	fillRoundedRect(img, 70, 1060, 960, 660, 34, rgbaA(35, 33, 37, 235))
	drawText(img, faces.Title, 110, 1130, rgba(245, 245, 245), "Map Statistic")
	fillRoundedRect(img, 100, 1180, 315, 260, 20, rgba(52, 50, 55))
	drawProgressRing(img, 180, 1275, 58, 8, float64(data.WinRate)/100.0, rgba(93, 92, 96), rgba(55, 223, 146))
	drawText(img, faces.Display, 246, 1272, rgba(55, 223, 146), fmt.Sprintf("%d%%", data.WinRate))
	drawText(img, faces.SubTitle, 246, 1310, rgba(245, 245, 245), "Win Rate")
	drawText(img, faces.SubTitle, 246, 1346, rgba(155, 155, 166), fmt.Sprintf("W = %d    L = %d", data.Wins, data.Losses))
	drawHorizontalBar(img, 126, 1388, 240, 10, float64(data.WinRate)/100.0, rgba(72, 81, 91), rgba(55, 223, 146))

	best := data.BestMap
	fillRoundedRect(img, 100, 1460, 315, 220, 20, rgba(52, 50, 55))
	drawText(img, faces.Title, 224, 1542, rgba(245, 245, 245), best.Name)
	drawText(img, faces.SubTitle, 228, 1592, rgba(196, 196, 196), fmt.Sprintf("W = %d     L = %d", best.Wins, best.Losses))
	drawText(img, faces.SubTitle, 228, 1634, rgba(196, 196, 196), fmt.Sprintf("K/D = %.2f", best.KD))
	drawText(img, faces.SubTitle, 228, 1676, rgba(196, 196, 196), fmt.Sprintf("W/R = %d%%", best.WR))

	mapStartX := 430
	mapStartY := 1180
	for idx, card := range data.MapCards {
		if idx >= 6 {
			break
		}
		col := idx % 3
		row := idx / 3
		x := mapStartX + col*185
		y := mapStartY + row*240
		fillRoundedRect(img, x, y, 170, 220, 18, rgba(52, 50, 55))
		drawText(img, faces.BodyBold, x+20, y+52, rgba(245, 245, 245), trimText(faces.BodyBold, card.Name, 130))
		drawText(img, faces.SubTitle, x+20, y+102, rgba(196, 196, 196), fmt.Sprintf("W = %d    L = %d", card.Wins, card.Losses))
		drawText(img, faces.SubTitle, x+20, y+146, rgba(196, 196, 196), fmt.Sprintf("K/D = %.2f", card.KD))
		drawText(img, faces.SubTitle, x+20, y+188, rgba(196, 196, 196), fmt.Sprintf("W/R = %d%%", card.WR))
	}

	fillRoundedRect(img, 1060, 1150, 470, 570, 34, rgbaA(35, 33, 37, 235))
	drawText(img, faces.Title, 1188, 1230, rgba(245, 245, 245), "Recent Matches")
	gridX, gridY := 1096, 1292
	for idx := 0; idx < 25; idx++ {
		col := idx % 5
		row := idx / 5
		x := gridX + col*78
		y := gridY + row*78
		result := ""
		if idx < len(data.RecentResults) {
			result = data.RecentResults[idx]
		}
		boxColor := rgba(52, 50, 55)
		textColor := rgba(80, 80, 86)
		if result == "W" {
			boxColor = rgba(15, 77, 49)
			textColor = rgba(79, 245, 155)
		}
		if result == "L" {
			boxColor = rgba(92, 27, 41)
			textColor = rgba(255, 96, 122)
		}
		fillRoundedRect(img, x, y, 64, 64, 10, boxColor)
		if result != "" {
			drawCenteredText(img, faces.BodyBold, x+32, y+42, textColor, result)
		}
		}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func updateMatchDerivedStats(matchID int) error {
	var mapName string
	var scoreCT, scoreT int
	if err := queryRowDB("SELECT COALESCE(map_name,''), score_ct, score_t FROM matches WHERE id = ?", matchID).Scan(&mapName, &scoreCT, &scoreT); err != nil {
		return err
	}
	rows, err := queryDB("SELECT user_id, team, kills, deaths, assists, rating FROM match_players WHERE match_id = ?", matchID)
	if err != nil {
		return err
	}
	defer rows.Close()
	bestUserID := 0
	bestRating := -1.0
	for rows.Next() {
		var userID, kills, deaths, assists int
		var team string
		var rating float64
		if err := rows.Scan(&userID, &team, &kills, &deaths, &assists, &rating); err != nil {
			continue
		}
		if rating <= 0 {
			rating = calculatePlayerRating(kills, deaths, assists)
			_, _ = execDB("UPDATE match_players SET rating = ? WHERE match_id = ? AND user_id = ?", rating, matchID, userID)
		}
		won := 0
		if (team == "CT" && scoreCT > scoreT) || (team == "T" && scoreT > scoreCT) {
			won = 1
		}
		lost := 0
		if won == 0 {
			lost = 1
		}
		var statID int
		err := queryRowDB("SELECT id FROM map_stats WHERE user_id = ? AND map_name = ?", userID, mapName).Scan(&statID)
		if err == sql.ErrNoRows {
			_, _ = execDB("INSERT INTO map_stats (user_id, map_name, wins, losses, kills, deaths) VALUES (?, ?, ?, ?, ?, ?)", userID, mapName, won, lost, kills, deaths)
		} else if err == nil {
			_, _ = execDB("UPDATE map_stats SET wins = wins + ?, losses = losses + ?, kills = kills + ?, deaths = deaths + ? WHERE id = ?", won, lost, kills, deaths, statID)
		}
		if rating > bestRating {
			bestRating = rating
			bestUserID = userID
		}
	}
	if bestUserID > 0 {
		_, _ = execDB("UPDATE users SET mvps = mvps + 1 WHERE id = ?", bestUserID)
	}
	return nil
}

// ── Pick/Ban Result Card ─────────────────────────────────────────────────

type pickCardPlayer struct {
	Name      string
	AvatarURL string
	Matches   int
	KD        float64
	Avg       float64
	Elo       int
}

type pickCardData struct {
	MatchID    int
	MapName    string
	MapEmoji   string
	Category   string
	HostName   string
	HostAvatar string
	HostGameID string
	CTCaptain  string
	TCaptain   string
	CTElo      int
	TElo       int
	WinPctCT   int
	WinPctT    int
	CTPlayers  []pickCardPlayer
	TPlayers   []pickCardPlayer
}

func buildPickCardData(s *discordgo.Session, state *PickBanState, mapInfo MapInfo) *pickCardData {
	loadPlayer := func(p PlayerInfo) pickCardPlayer {
		card := pickCardPlayer{Name: p.Username}
		var matches, wins, kills, deaths int
		_ = queryRowDB("SELECT games, wins, kills, deaths, COALESCE(NULLIF(game_nick,''), username) FROM users WHERE id = ?", p.UniqueID).Scan(&matches, &wins, &kills, &deaths, &card.Name)
		card.Matches = matches
		if deaths > 0 {
			card.KD = math.Round(float64(kills)/float64(deaths)*100) / 100
		}
		if matches > 0 {
			card.Avg = math.Round(float64(kills)/float64(matches)*100) / 100
		}
		_ = queryRowDB("SELECT elo FROM users WHERE id = ?", p.UniqueID).Scan(&card.Elo)
		discordID := ""
		_ = queryRowDB("SELECT discord_id FROM users WHERE id = ?", p.UniqueID).Scan(&discordID)
		if discordID != "" {
			card.AvatarURL = findMemberAvatarURL(s, discordID)
		}
		return card
	}

	data := &pickCardData{
		MatchID:  state.MatchID,
		MapName:  mapInfo.Name,
		MapEmoji: mapInfo.Emoji,
		Category: state.League,
	}

	var ctTotalElo, tTotalElo, ctCount, tCount int
	for _, p := range state.CTTeam {
		pc := loadPlayer(p)
		data.CTPlayers = append(data.CTPlayers, pc)
		ctTotalElo += pc.Elo
		ctCount++
		if p.IsCaptain {
			data.CTCaptain = pc.Name
			data.HostName = pc.Name
			data.HostAvatar = pc.AvatarURL
			_ = queryRowDB("SELECT COALESCE(game_id,'') FROM users WHERE id = ?", p.UniqueID).Scan(&data.HostGameID)
		}
	}
	for _, p := range state.TTeam {
		pc := loadPlayer(p)
		data.TPlayers = append(data.TPlayers, pc)
		tTotalElo += pc.Elo
		tCount++
		if p.IsCaptain {
			data.TCaptain = pc.Name
		}
	}
	if ctCount > 0 { data.CTElo = ctTotalElo / ctCount }
	if tCount > 0 { data.TElo = tTotalElo / tCount }
	total := data.CTElo + data.TElo
	if total > 0 {
		data.WinPctCT = data.CTElo * 100 / total
		data.WinPctT = data.TElo * 100 / total
	}
	return data
}

func renderPickCardPNG(s *discordgo.Session, state *PickBanState, mapInfo MapInfo) ([]byte, error) {
	data := buildPickCardData(s, state, mapInfo)
	faces := loadCardFaces()

	W, H := 1600, 900
	img := image.NewRGBA(image.Rect(0, 0, W, H))

	// Background
	fillGradient(img, 0, 0, W, H, rgba(42, 8, 60), rgba(8, 55, 10), false)

	// Left panel — CT (purple)
	fillRoundedRect(img, 10, 10, 490, H-20, 24, rgbaA(70, 20, 90, 230))
	drawText(img, faces.Small, 28, 44, rgba(190, 180, 200), "Counter-Terrorists")
	fillRoundedRect(img, 28, 54, 70, 70, 14, rgba(50, 30, 65))

	ctCapName := data.CTCaptain
	if ctCapName == "" && len(data.CTPlayers) > 0 { ctCapName = data.CTPlayers[0].Name }
	drawText(img, faces.Title, 112, 92, rgba(245, 245, 245), trimText(faces.Title, ctCapName, 320))
	ctWinStr := fmt.Sprintf("%d%%", data.WinPctCT)
	ctEloStr := fmt.Sprintf("%d", data.CTElo)
	drawText(img, faces.Small, 112, 118, rgba(170, 160, 180), "Victory %")
	drawText(img, faces.BodyBold, 220, 118, rgba(245, 245, 245), ctWinStr)
	drawText(img, faces.Small, 300, 118, rgba(170, 160, 180), "ELO")
	drawText(img, faces.BodyBold, 345, 118, rgba(195, 71, 255), ctEloStr)
	drawRoundedImageFromURL(img, data.HostAvatar, 28, 54, 70, 70, 14, rgba(70, 50, 85))

	for idx, p := range data.CTPlayers {
		y := 165 + idx*120
		if y+110 > H-20 { break }
		fillRoundedRect(img, 20, y, 470, 108, 18, rgbaA(55, 25, 70, 220))
		drawRoundedImageFromURL(img, p.AvatarURL, 32, y+16, 72, 72, 14, rgba(70, 50, 85))
		drawText(img, faces.Small, 118, y+34, rgba(190, 180, 200), "Team")
		drawText(img, faces.BodyBold, 118, y+62, rgba(245, 245, 245), trimText(faces.BodyBold, p.Name, 230))
		drawText(img, faces.Tiny, 118, y+88, rgba(140, 130, 155), fmt.Sprintf("Matches %d", p.Matches))
		drawText(img, faces.Tiny, 220, y+88, rgba(140, 130, 155), fmt.Sprintf("K/D %.2f", p.KD))
		drawText(img, faces.Tiny, 328, y+88, rgba(140, 130, 155), fmt.Sprintf("AVG %.0f", p.Avg))
	}

	// Right panel — T (green)
	fillRoundedRect(img, W-500, 10, 490, H-20, 24, rgbaA(10, 60, 15, 230))
	drawRightText(img, faces.Small, W-28, 44, rgba(170, 200, 170), "Terrorists")
	tCapName := data.TCaptain
	if tCapName == "" && len(data.TPlayers) > 0 { tCapName = data.TPlayers[0].Name }
	drawRightText(img, faces.Title, W-28, 92, rgba(245, 245, 245), trimText(faces.Title, tCapName, 320))
	tWinStr := fmt.Sprintf("%d%%", data.WinPctT)
	tEloStr := fmt.Sprintf("%d", data.TElo)
	drawRightText(img, faces.Small, W-180, 118, rgba(170, 200, 170), "ELO")
	drawRightText(img, faces.BodyBold, W-120, 118, rgba(55, 200, 100), tEloStr)
	drawRightText(img, faces.Small, W-290, 118, rgba(170, 200, 170), "Victory %")
	drawRightText(img, faces.BodyBold, W-230, 118, rgba(245, 245, 245), tWinStr)
	if len(data.TPlayers) > 0 { drawRoundedImageFromURL(img, data.TPlayers[0].AvatarURL, W-98, 54, 70, 70, 14, rgba(10, 70, 20)) }

	for idx, p := range data.TPlayers {
		y := 165 + idx*120
		if y+110 > H-20 { break }
		fillRoundedRect(img, W-490, y, 470, 108, 18, rgbaA(15, 65, 20, 220))
		drawRoundedImageFromURL(img, p.AvatarURL, W-490+12, y+16, 72, 72, 14, rgba(10, 70, 20))
		drawText(img, faces.Small, W-490+98, y+34, rgba(170, 200, 170), "Team")
		drawText(img, faces.BodyBold, W-490+98, y+62, rgba(245, 245, 245), trimText(faces.BodyBold, p.Name, 230))
		drawText(img, faces.Tiny, W-490+98, y+88, rgba(130, 155, 135), fmt.Sprintf("Matches %d", p.Matches))
		drawText(img, faces.Tiny, W-490+200, y+88, rgba(130, 155, 135), fmt.Sprintf("K/D %.2f", p.KD))
		drawText(img, faces.Tiny, W-490+308, y+88, rgba(130, 155, 135), fmt.Sprintf("AVG %.0f", p.Avg))
	}

	// Center panel
	centerX := 505
	centerW := W - 500 - centerX
	fillRoundedRect(img, centerX, 10, centerW, H-20, 24, rgbaA(28, 28, 32, 240))

	// Map header
	drawCenteredText(img, faces.Small, centerX+centerW/2, 48, rgba(190, 190, 195), "Map")
	drawCenteredText(img, faces.Title, centerX+centerW/2, 86, rgba(245, 245, 245), data.MapName)

	// ELITE FPL logo text center
	drawCenteredText(img, faces.BodyBold, centerX+centerW/2, 128, rgba(190, 71, 255), "ELITE")
	drawCenteredText(img, faces.BodyBold, centerX+centerW/2, 152, rgba(55, 200, 100), "FPL")

	// League + format
	leagueShort := strings.TrimPrefix(strings.TrimPrefix(data.Category, "🔴 | "), "🟣 | ")
	leagueShort = strings.TrimPrefix(strings.TrimPrefix(leagueShort, "🟢 | "), "⚪ | ")
	fillRoundedRect(img, centerX+20, 180, centerW-40, 60, 18, rgba(44, 44, 50))
	drawCenteredText(img, faces.Body, centerX+centerW/2-80, 218, rgba(245, 245, 245), leagueShort)
	drawText(img, faces.BodyBold, centerX+centerW/2+10, 218, rgba(195, 71, 255), "BO")
	drawText(img, faces.BodyBold, centerX+centerW/2+45, 218, rgba(245, 245, 245), "Best of 1")

	// Host block
	fillRoundedRect(img, centerX+20, 258, centerW-40, 110, 18, rgba(44, 44, 50))
	drawRoundedImageFromURL(img, data.HostAvatar, centerX+36, 274, 72, 72, 14, rgba(80, 70, 90))
	drawText(img, faces.Small, centerX+120, 300, rgba(190, 190, 195), "Host")
	drawText(img, faces.SubTitle, centerX+120, 332, rgba(245, 245, 245), trimText(faces.SubTitle, data.HostName, 180))
	hostIDStr := data.HostGameID
	if hostIDStr == "" { hostIDStr = "—" }
	drawText(img, faces.Small, centerX+120+textWidth(faces.SubTitle, trimText(faces.SubTitle, data.HostName, 180))+16, 300, rgba(140, 140, 150), "ID")
	drawText(img, faces.SubTitle, centerX+120+textWidth(faces.SubTitle, trimText(faces.SubTitle, data.HostName, 180))+36, 332, rgba(190, 190, 195), hostIDStr)
	drawText(img, faces.Tiny, centerX+36, 358, rgba(140, 140, 150), "ℹ Хост создаёт лобби в игре")

	// ELO bar
	fillRoundedRect(img, centerX+20, 388, centerW-40, 90, 18, rgba(44, 44, 50))
	barX := centerX + 24
	barW := centerW - 48
	drawText(img, faces.SubTitle, barX, 420, rgba(195, 71, 255), fmt.Sprintf("%d", data.CTElo))
	drawRightText(img, faces.SubTitle, barX+barW, 420, rgba(55, 200, 100), fmt.Sprintf("%d", data.TElo))
	drawCenteredText(img, faces.Small, centerX+centerW/2, 420, rgba(190, 190, 195), "ELO")
	barY := 432
	barH := 18
	fillRoundedRect(img, barX, barY, barW, barH, barH/2, rgba(55, 200, 100))
	ctBarW := barW * data.WinPctCT / 100
	if ctBarW < 8 { ctBarW = 8 }
	fillRoundedRect(img, barX, barY, ctBarW, barH, barH/2, rgba(195, 71, 255))
	drawText(img, faces.Tiny, barX, barY+barH+18, rgba(140, 140, 150), "ℹ Играется строго эта карта")

	// Players count
	drawCenteredText(img, faces.Body, centerX+centerW/2, 488, rgba(190, 190, 195), fmt.Sprintf("5vs5 · Матч #%d", data.MatchID))

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
