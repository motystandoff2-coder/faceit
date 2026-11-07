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
	tpl := DefaultMatchCardTemplate
	w, h := tpl.CanvasW, tpl.CanvasH
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillGradient(img, 0, 0, w, h, tpl.BackgroundLeft, tpl.BackgroundRight, false)
	fillRoundedRect(img, tpl.Frame.X, tpl.Frame.Y, tpl.Frame.W, tpl.Frame.H, tpl.Frame.R, tpl.FrameColor)
	drawText(img, faces.DisplayLarge, tpl.MatchIDX, tpl.MatchIDBaseline, rgba(148, 133, 160), fmt.Sprintf("#%d", data.MatchID))
	drawRightText(img, faces.Title, tpl.LogoRight, tpl.LogoBaseline, tpl.TextPrimary, "ELITE FPL")

	fillRoundedGradientRect(img, tpl.Header.X, tpl.Header.Y, tpl.Header.W, tpl.Header.H, tpl.Header.R, tpl.HeaderLeft, tpl.HeaderRight, false)
	fillRoundedRect(img, tpl.HeaderLeftIcon.X, tpl.HeaderLeftIcon.Y, tpl.HeaderLeftIcon.W, tpl.HeaderLeftIcon.H, tpl.HeaderLeftIcon.R, rgba(40, 35, 48))
	fillRoundedRect(img, tpl.HeaderRightIcon.X, tpl.HeaderRightIcon.Y, tpl.HeaderRightIcon.W, tpl.HeaderRightIcon.H, tpl.HeaderRightIcon.R, rgba(40, 35, 48))
	drawText(img, faces.SubTitle, tpl.HeaderLeftIcon.X+92, tpl.Header.Y+58, rgba(235, 223, 242), "COUNTER-TERRORISTS")
	drawRightText(img, faces.SubTitle, tpl.Header.X+tpl.Header.W-104, tpl.Header.Y+58, rgba(219, 242, 219), "TERRORISTS")
	drawCenteredText(img, faces.BodyBold, w/2, tpl.ScoreTitleY, tpl.TextMuted, "SCORE")
	drawCenteredText(img, faces.Display, w/2, tpl.ScoreValueY, tpl.TextPrimary, fmt.Sprintf("%d:%d", data.ScoreCT, data.ScoreT))

	leftX := tpl.LeftTableX
	rightX := tpl.RightTableX
	tableY := tpl.TableHeaderY
	rowH := tpl.TableRowH
	playerW := tpl.PlayerW
	statW := tpl.StatW
	gap := tpl.Gap
	labelColor := tpl.TextPrimary
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
			y := tableY + tpl.TableRowOffsetY + idx*rowH
			if isLeft {
				fillRoundedRect(img, leftX, y, playerW, 64, 18, rgba(47, 44, 49))
				for col := 0; col < 5; col++ {
					boxX := leftX + playerW + gap + col*(statW+gap)
					shade := rgba(49, 45, 53)
					if col == 4 {
						shade = tpl.AccentCT
					}
					fillRoundedRect(img, boxX, y, statW, 64, 14, shade)
				}
				if row.Name != "" {
					nameColor := tpl.TextPrimary
					if idx == 0 {
						nameColor = tpl.AccentHighlight
					}
					drawText(img, faces.SubTitle, leftX+18, y+42, nameColor, trimText(faces.SubTitle, row.Name, playerW-30))
					vals := []string{fmt.Sprintf("%d", row.Kills), fmt.Sprintf("%d", row.Deaths), fmt.Sprintf("%.2f", row.KD), fmt.Sprintf("%d", row.Assists), fmt.Sprintf("%.2f", row.Rating)}
					for col, val := range vals {
						boxX := leftX + playerW + gap + col*(statW+gap)
						drawCenteredText(img, faces.SubTitle, boxX+statW/2, y+44, tpl.TextPrimary, val)
					}
				}
			} else {
				for col := 0; col < 5; col++ {
					boxX := rightX + col*(statW+gap)
					shade := rgba(47, 52, 46)
					if col == 0 {
						shade = tpl.AccentT
					}
					fillRoundedRect(img, boxX, y, statW, 64, 14, shade)
				}
				nameX := rightX + 5*(statW+gap)
				fillRoundedRect(img, nameX, y, playerW, 64, 18, rgba(47, 44, 49))
				if row.Name != "" {
					vals := []string{fmt.Sprintf("%d", row.Kills), fmt.Sprintf("%d", row.Deaths), fmt.Sprintf("%.2f", row.KD), fmt.Sprintf("%d", row.Assists), fmt.Sprintf("%.2f", row.Rating)}
					for col, val := range vals {
						boxX := rightX + col*(statW+gap)
						drawCenteredText(img, faces.SubTitle, boxX+statW/2, y+44, tpl.TextPrimary, val)
					}
					drawRightText(img, faces.SubTitle, nameX+playerW-18, y+42, tpl.TextPrimary, trimText(faces.SubTitle, row.Name, playerW-30))
				}
			}
		}
	}
	drawMatchRows(data.CTPlayers, true)
	drawMatchRows(data.TPlayers, false)

	drawText(img, faces.Body, tpl.BottomLeftCard.X, tpl.BottomLeftTitleY, rgba(226, 226, 226), "MAP INFO")
	fillRoundedRect(img, tpl.BottomLeftCard.X, tpl.BottomLeftCard.Y, tpl.BottomLeftCard.W, tpl.BottomLeftCard.H, tpl.BottomLeftCard.R, tpl.PanelColor)
	drawCenteredText(img, faces.Italic, tpl.BottomLeftCard.X+tpl.BottomLeftCard.W/2, tpl.BottomLeftCard.Y+62, rgba(192, 192, 192), "Picked map")
	drawCenteredText(img, faces.Display, tpl.BottomLeftCard.X+tpl.BottomLeftCard.W/2, tpl.BottomLeftCard.Y+114, tpl.TextPrimary, data.MapName)

	drawText(img, faces.Body, tpl.BottomRightCard.X, tpl.BottomLeftTitleY, rgba(226, 226, 226), "PLAYER INFO")
	drawText(img, faces.Body, tpl.MVPStat1.X+14, tpl.BottomLeftTitleY, rgba(226, 226, 226), "RATING")
	drawText(img, faces.Body, tpl.MVPStat2.X+28, tpl.BottomLeftTitleY, rgba(226, 226, 226), "K/D")
	drawText(img, faces.Body, tpl.MVPStat3.X+36, tpl.BottomLeftTitleY, rgba(226, 226, 226), "A")
	fillRoundedRect(img, tpl.BottomRightCard.X, tpl.BottomRightCard.Y, tpl.BottomRightCard.W, tpl.BottomRightCard.H, tpl.BottomRightCard.R, tpl.PanelColor)
	drawRoundedImageFromURL(img, data.MVP.AvatarURL, tpl.MVPAvatar.X, tpl.MVPAvatar.Y, tpl.MVPAvatar.W, tpl.MVPAvatar.H, tpl.MVPAvatar.R, rgba(52, 48, 55))
	drawText(img, faces.Italic, tpl.MVPAvatar.X+172, tpl.MVPAvatar.Y+52, rgba(198, 198, 198), "Game MVP")
	drawText(img, faces.Display, tpl.MVPAvatar.X+172, tpl.MVPAvatar.Y+102, tpl.TextPrimary, trimText(faces.Display, data.MVP.Name, 320))
	fillRoundedRect(img, tpl.MVPStat1.X, tpl.MVPStat1.Y, tpl.MVPStat1.W, tpl.MVPStat1.H, tpl.MVPStat1.R, rgba(28, 24, 34))
	fillRoundedRect(img, tpl.MVPStat2.X, tpl.MVPStat2.Y, tpl.MVPStat2.W, tpl.MVPStat2.H, tpl.MVPStat2.R, rgba(28, 24, 34))
	fillRoundedRect(img, tpl.MVPStat3.X, tpl.MVPStat3.Y, tpl.MVPStat3.W, tpl.MVPStat3.H, tpl.MVPStat3.R, rgba(28, 24, 34))
	drawCenteredText(img, faces.Display, tpl.MVPStat1.X+tpl.MVPStat1.W/2, tpl.MVPStat1.Y+80, tpl.TextPrimary, fmt.Sprintf("%.2f", data.MVPRating))
	drawCenteredText(img, faces.Display, tpl.MVPStat2.X+tpl.MVPStat2.W/2, tpl.MVPStat2.Y+80, tpl.TextPrimary, fmt.Sprintf("%.2f", data.MVPKD))
	drawCenteredText(img, faces.Display, tpl.MVPStat3.X+tpl.MVPStat3.W/2, tpl.MVPStat3.Y+80, tpl.TextPrimary, fmt.Sprintf("%d", data.MVPAssists))

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
	tpl := DefaultProfileCardTemplate
	w, h := tpl.CanvasW, tpl.CanvasH
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillGradient(img, 0, 0, w, h, tpl.BackgroundLeft, tpl.BackgroundRight, false)

	fillRoundedRect(img, tpl.Banner.X, tpl.Banner.Y, tpl.Banner.W, tpl.Banner.H, tpl.Banner.R, tpl.PanelColor)
	drawRoundedImageFromURL(img, data.AvatarURL, tpl.BannerAvatar.X, tpl.BannerAvatar.Y, tpl.BannerAvatar.W, tpl.BannerAvatar.H, tpl.BannerAvatar.R, rgba(70, 70, 75))
	drawText(img, faces.Display, tpl.BannerNameX, tpl.BannerNameY, tpl.TextPrimary, trimText(faces.Display, data.DisplayName, 420))
	drawText(img, faces.SubTitle, tpl.BannerNameX, tpl.BannerMetaY, tpl.TextMuted, fmt.Sprintf("#%d", data.ProjectID))
	drawText(img, faces.SubTitle, tpl.BannerGameIDX, tpl.BannerMetaY, tpl.TextMuted, fmt.Sprintf("ID: %s", data.GameID))
	for i := 0; i < 4; i++ {
		fillRoundedRect(img, 1240+i*64, tpl.TopIconY, tpl.TopIconSize, tpl.TopIconSize, 12, rgba(54, 52, 57))
	}

	leftPanelX, leftPanelY := tpl.StatPanel.X, tpl.StatPanel.Y
	leftPanelW, leftPanelH := tpl.StatPanel.W, tpl.StatPanel.H
	fillRoundedRect(img, leftPanelX, leftPanelY, leftPanelW, leftPanelH, tpl.StatPanel.R, tpl.PanelColor)
	drawText(img, faces.Title, leftPanelX+110, leftPanelY+66, rgba(241, 241, 241), "Statistic")
	fillRoundedRect(img, tpl.MainStatCard.X, tpl.MainStatCard.Y, tpl.MainStatCard.W, tpl.MainStatCard.H, tpl.MainStatCard.R, tpl.PanelDark)
	drawProgressRing(img, tpl.MainStatCard.X+68, tpl.MainStatCard.Y+92, 46, 8, float64(data.Elo)/float64(maxInt(1000, data.LevelTarget)), rgba(93, 92, 96), tpl.AccentPurple)
	drawText(img, faces.Display, tpl.MainStatCard.X+125, tpl.MainStatCard.Y+92, tpl.TextPrimary, fmt.Sprintf("%d", data.Elo))
	drawRightText(img, faces.SubTitle, tpl.MainStatCard.X+tpl.MainStatCard.W-20, tpl.MainStatCard.Y+92, tpl.TextPrimary, fmt.Sprintf("#%d", maxInt(1, data.Rank)))
	drawText(img, faces.SubTitle, tpl.MainStatCard.X+125, tpl.MainStatCard.Y+136, tpl.TextMuted, fmt.Sprintf("%d / %d", data.Elo, data.LevelTarget))
	drawText(img, faces.SubTitle, tpl.MainStatCard.X+330, tpl.MainStatCard.Y+136, tpl.AccentPurple, fmt.Sprintf("%d до уровня", data.LevelRemaining))
	drawHorizontalBar(img, tpl.MainStatCard.X+125, tpl.MainStatCard.Y+150, 205, 12, float64(data.Elo)/float64(maxInt(1000, data.LevelTarget)), rgba(72, 81, 91), tpl.AccentPurple)

	fillRoundedRect(img, tpl.KDCard.X, tpl.KDCard.Y, tpl.KDCard.W, tpl.KDCard.H, tpl.KDCard.R, tpl.PanelDark)
	drawProgressRing(img, tpl.KDCard.X+68, tpl.KDCard.Y+92, 52, 9, clampFloat(data.KD/2.0, 0, 1), rgba(93, 92, 96), tpl.AccentGreen)
	drawText(img, faces.Display, tpl.KDCard.X+115, tpl.KDCard.Y+82, tpl.AccentGreen, fmt.Sprintf("%.2f", data.KD))
	drawText(img, faces.SubTitle, tpl.KDCard.X+115, tpl.KDCard.Y+120, tpl.TextPrimary, "Рейтинг")
	drawText(img, faces.SubTitle, tpl.KDCard.X+115, tpl.KDCard.Y+160, tpl.TextMuted, fmt.Sprintf("K/D %.2f   AVG %.0f", data.KD, data.Avg))
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
		cx := tpl.KDCard.X + 20 + (idx%3)*145
		cy := tpl.KDCard.Y + 188 + (idx/3)*82
		fillRoundedRect(img, cx, cy, 132, 70, 12, rgba(62, 60, 65))
		drawText(img, faces.Small, cx+12, cy+24, rgba(187, 187, 192), cell.Label)
		drawText(img, faces.SubTitle, cx+12, cy+60, cell.Color, cell.Value)
	}

	fillRoundedRect(img, tpl.HistoryCard.X, tpl.HistoryCard.Y, tpl.HistoryCard.W, tpl.HistoryCard.H, tpl.HistoryCard.R, tpl.PanelDark)
	drawText(img, faces.BodyBold, tpl.HistoryCard.X+20, tpl.HistoryCard.Y+54, tpl.TextPrimary, "История рейтинга")
	currentBarX := tpl.HistoryCard.X + 23
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
			drawText(img, faces.Display, tpl.HistoryCard.X+20, tpl.HistoryCard.Y+116, tpl.TextPrimary, fmt.Sprintf("%d", data.Elo))
			if len(data.RatingBars) > 1 {
				delta := data.RatingBars[len(data.RatingBars)-1] - data.RatingBars[0]
				colorDelta := tpl.AccentRed
				if delta >= 0 {
					colorDelta = tpl.AccentGreen
				}
				drawText(img, faces.SubTitle, tpl.HistoryCard.X+150, tpl.HistoryCard.Y+116, colorDelta, fmt.Sprintf("%+d", delta))
			}
			barBaseY := tpl.HistoryCard.Y + 284
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
	drawText(img, faces.Body, tpl.HistoryCard.X+20, tpl.HistoryCard.Y+336, tpl.TextMuted, "27 сен.")
	drawRightText(img, faces.Body, tpl.HistoryCard.X+tpl.HistoryCard.W-20, tpl.HistoryCard.Y+336, tpl.TextMuted, time.Now().Format("02 янв."))

	fillRoundedRect(img, tpl.ImpactCard.X, tpl.ImpactCard.Y, tpl.ImpactCard.W, tpl.ImpactCard.H, tpl.ImpactCard.R, tpl.PanelDark)
	drawProgressRing(img, tpl.ImpactCard.X+85, tpl.ImpactCard.Y+54, 40, 8, clampFloat(data.Impact/2.0, 0, 1), rgba(93, 92, 96), tpl.AccentGreen)
	drawText(img, faces.Display, tpl.ImpactCard.X+140, tpl.ImpactCard.Y+48, tpl.AccentGreen, fmt.Sprintf("%.2f", data.Impact))
	drawText(img, faces.SubTitle, tpl.ImpactCard.X+140, tpl.ImpactCard.Y+82, tpl.TextPrimary, "Impact")
	drawText(img, faces.SubTitle, tpl.ImpactCard.X+140, tpl.ImpactCard.Y+114, tpl.TextMuted, "Влияние на раунд")

	fillRoundedRect(img, tpl.InfoPanel.X, tpl.InfoPanel.Y, tpl.InfoPanel.W, tpl.InfoPanel.H, tpl.InfoPanel.R, tpl.PanelGreen)
	statsTop := []struct{ Label, Value string }{
		{"Playtime", fmt.Sprintf("%dh", data.PlayHours)},
		{"Join Date", data.JoinedAt.Format("02.01.2006")},
		{"Games", fmt.Sprintf("%d", data.Games)},
		{"MVP", fmt.Sprintf("%d", data.MVPs)},
	}
	for idx, item := range statsTop {
		x := tpl.InfoPanel.X + 100 + (idx%2)*200
		y := tpl.InfoPanel.Y + 68 + (idx/2)*92
		drawText(img, faces.SubTitle, x, y, rgba(196, 196, 196), item.Label)
		drawText(img, faces.Display, x, y+44, tpl.TextPrimary, item.Value)
	}

	fillRoundedRect(img, tpl.LeaguePanel.X, tpl.LeaguePanel.Y, tpl.LeaguePanel.W, tpl.LeaguePanel.H, tpl.LeaguePanel.R, tpl.PanelColor)
	drawText(img, faces.SubTitle, tpl.LeaguePanel.X+38, tpl.LeaguePanel.Y+60, rgba(196, 196, 196), "League")
	drawText(img, faces.Title, tpl.LeaguePanel.X+38, tpl.LeaguePanel.Y+122, tpl.TextPrimary, data.League)
	drawText(img, faces.SubTitle, tpl.LeaguePanel.X+38, tpl.LeaguePanel.Y+170, rgba(196, 196, 196), "Places")
	for idx, item := range data.Leaderboard {
		y := tpl.LeaguePanel.Y + 242 + idx*100
		drawText(img, faces.SubTitle, tpl.LeaguePanel.X+40, y, tpl.TextPrimary, fmt.Sprintf("#%d", idx+1))
		drawRoundedImageFromURL(img, item.AvatarURL, tpl.LeaguePanel.X+98, y-52, 64, 64, 14, rgba(72, 70, 75))
		drawText(img, faces.SubTitle, tpl.LeaguePanel.X+180, y, tpl.TextPrimary, trimText(faces.SubTitle, item.Name, 220))
	}
	fillRoundedRect(img, tpl.LeaguePanel.X+34, tpl.LeaguePanel.Y+424, tpl.LeaguePanel.W-74, 2, 0, tpl.AccentLine)
	drawText(img, faces.Display, tpl.LeaguePanel.X+38, tpl.LeaguePanel.Y+502, tpl.TextPrimary, fmt.Sprintf("#%d", maxInt(1, data.Rank)))
	drawRoundedImageFromURL(img, data.AvatarURL, tpl.LeaguePanel.X+98, tpl.LeaguePanel.Y+446, 64, 64, 14, rgba(72, 70, 75))
	drawText(img, faces.SubTitle, tpl.LeaguePanel.X+180, tpl.LeaguePanel.Y+496, tpl.AccentGreen, trimText(faces.SubTitle, data.DisplayName, 220))

	fillRoundedRect(img, tpl.MapsPanel.X, tpl.MapsPanel.Y, tpl.MapsPanel.W, tpl.MapsPanel.H, tpl.MapsPanel.R, tpl.PanelColor)
	drawText(img, faces.Title, tpl.MapsPanel.X+40, tpl.MapsPanel.Y+70, tpl.TextPrimary, "Map Statistic")
	fillRoundedRect(img, tpl.MapOverviewCard.X, tpl.MapOverviewCard.Y, tpl.MapOverviewCard.W, tpl.MapOverviewCard.H, tpl.MapOverviewCard.R, tpl.PanelDark)
	drawProgressRing(img, tpl.MapOverviewCard.X+80, tpl.MapOverviewCard.Y+95, 58, 8, float64(data.WinRate)/100.0, rgba(93, 92, 96), tpl.AccentGreen)
	drawText(img, faces.Display, tpl.MapOverviewCard.X+146, tpl.MapOverviewCard.Y+92, tpl.AccentGreen, fmt.Sprintf("%d%%", data.WinRate))
	drawText(img, faces.SubTitle, tpl.MapOverviewCard.X+146, tpl.MapOverviewCard.Y+130, tpl.TextPrimary, "Win Rate")
	drawText(img, faces.SubTitle, tpl.MapOverviewCard.X+146, tpl.MapOverviewCard.Y+166, tpl.TextMuted, fmt.Sprintf("W = %d    L = %d", data.Wins, data.Losses))
	drawHorizontalBar(img, tpl.MapOverviewCard.X+26, tpl.MapOverviewCard.Y+208, 240, 10, float64(data.WinRate)/100.0, rgba(72, 81, 91), tpl.AccentGreen)

	best := data.BestMap
	fillRoundedRect(img, tpl.BestMapCard.X, tpl.BestMapCard.Y, tpl.BestMapCard.W, tpl.BestMapCard.H, tpl.BestMapCard.R, tpl.PanelDark)
	drawText(img, faces.Title, tpl.BestMapCard.X+124, tpl.BestMapCard.Y+82, tpl.TextPrimary, best.Name)
	drawText(img, faces.SubTitle, tpl.BestMapCard.X+128, tpl.BestMapCard.Y+132, rgba(196, 196, 196), fmt.Sprintf("W = %d     L = %d", best.Wins, best.Losses))
	drawText(img, faces.SubTitle, tpl.BestMapCard.X+128, tpl.BestMapCard.Y+174, rgba(196, 196, 196), fmt.Sprintf("K/D = %.2f", best.KD))
	drawText(img, faces.SubTitle, tpl.BestMapCard.X+128, tpl.BestMapCard.Y+216, rgba(196, 196, 196), fmt.Sprintf("W/R = %d%%", best.WR))

	mapStartX := tpl.MapGridX
	mapStartY := tpl.MapGridY
	for idx, card := range data.MapCards {
		if idx >= 6 {
			break
		}
		col := idx % 3
		row := idx / 3
		x := mapStartX + col*tpl.MapGridGapX
		y := mapStartY + row*tpl.MapGridGapY
		fillRoundedRect(img, x, y, tpl.MapGridW, tpl.MapGridH, 18, tpl.PanelDark)
		drawText(img, faces.BodyBold, x+20, y+52, tpl.TextPrimary, trimText(faces.BodyBold, card.Name, 130))
		drawText(img, faces.SubTitle, x+20, y+102, rgba(196, 196, 196), fmt.Sprintf("W = %d    L = %d", card.Wins, card.Losses))
		drawText(img, faces.SubTitle, x+20, y+146, rgba(196, 196, 196), fmt.Sprintf("K/D = %.2f", card.KD))
		drawText(img, faces.SubTitle, x+20, y+188, rgba(196, 196, 196), fmt.Sprintf("W/R = %d%%", card.WR))
	}

	fillRoundedRect(img, tpl.RecentPanel.X, tpl.RecentPanel.Y, tpl.RecentPanel.W, tpl.RecentPanel.H, tpl.RecentPanel.R, tpl.PanelColor)
	drawText(img, faces.Title, tpl.RecentPanel.X+128, tpl.RecentPanel.Y+80, tpl.TextPrimary, "Recent Matches")
	gridX, gridY := tpl.RecentGridX, tpl.RecentGridY
	for idx := 0; idx < tpl.RecentCols*tpl.RecentRows; idx++ {
		col := idx % tpl.RecentCols
		row := idx / tpl.RecentCols
		x := gridX + col*tpl.RecentGap
		y := gridY + row*tpl.RecentGap
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
		fillRoundedRect(img, x, y, tpl.RecentCell, tpl.RecentCell, 10, boxColor)
		if result != "" {
			drawCenteredText(img, faces.BodyBold, x+tpl.RecentCell/2, y+42, textColor, result)
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
	tpl := DefaultPickCardTemplate

	W, H := tpl.CanvasW, tpl.CanvasH
	img := image.NewRGBA(image.Rect(0, 0, W, H))

	// Background
	fillGradient(img, 0, 0, W, H, tpl.BackgroundLeft, tpl.BackgroundRight, false)

	// Left panel — CT (purple)
	fillRoundedRect(img, tpl.LeftPanel.X, tpl.LeftPanel.Y, tpl.LeftPanel.W, tpl.LeftPanel.H, tpl.LeftPanel.R, tpl.CTPanelColor)
	drawText(img, faces.Small, tpl.LeftPanel.X+18, tpl.LeftPanel.Y+34, tpl.SoftCT, "Counter-Terrorists")
	fillRoundedRect(img, tpl.LeftPanel.X+18, tpl.LeftPanel.Y+44, 70, 70, 14, rgba(50, 30, 65))

	ctCapName := data.CTCaptain
	if ctCapName == "" && len(data.CTPlayers) > 0 { ctCapName = data.CTPlayers[0].Name }
	drawText(img, faces.Title, tpl.LeftPanel.X+102, tpl.LeftPanel.Y+82, tpl.TextPrimary, trimText(faces.Title, ctCapName, 320))
	ctWinStr := fmt.Sprintf("%d%%", data.WinPctCT)
	ctEloStr := fmt.Sprintf("%d", data.CTElo)
	drawText(img, faces.Small, tpl.LeftPanel.X+102, tpl.LeftPanel.Y+108, tpl.SoftCT, "Victory %")
	drawText(img, faces.BodyBold, tpl.LeftPanel.X+210, tpl.LeftPanel.Y+108, tpl.TextPrimary, ctWinStr)
	drawText(img, faces.Small, tpl.LeftPanel.X+290, tpl.LeftPanel.Y+108, tpl.SoftCT, "ELO")
	drawText(img, faces.BodyBold, tpl.LeftPanel.X+335, tpl.LeftPanel.Y+108, tpl.CTAccent, ctEloStr)
	drawRoundedImageFromURL(img, data.HostAvatar, tpl.LeftPanel.X+18, tpl.LeftPanel.Y+44, 70, 70, 14, rgba(70, 50, 85))

	for idx, p := range data.CTPlayers {
		y := tpl.SideCardTop + idx*tpl.SideCardGap
		if y+tpl.SideCardH > H-20 { break }
		fillRoundedRect(img, tpl.LeftPanel.X+tpl.SideCardXPad, y, tpl.SideCardW, tpl.SideCardH, 18, rgbaA(55, 25, 70, 220))
		drawRoundedImageFromURL(img, p.AvatarURL, tpl.LeftPanel.X+tpl.SideCardXPad+12, y+16, 72, 72, 14, rgba(70, 50, 85))
		textX := tpl.LeftPanel.X + tpl.SideCardXPad + 98
		drawText(img, faces.Small, textX, y+34, tpl.SoftCT, "Team")
		drawText(img, faces.BodyBold, textX, y+62, tpl.TextPrimary, trimText(faces.BodyBold, p.Name, 230))
		drawText(img, faces.Tiny, textX, y+88, rgba(140, 130, 155), fmt.Sprintf("Matches %d", p.Matches))
		drawText(img, faces.Tiny, textX+102, y+88, rgba(140, 130, 155), fmt.Sprintf("K/D %.2f", p.KD))
		drawText(img, faces.Tiny, textX+210, y+88, rgba(140, 130, 155), fmt.Sprintf("AVG %.0f", p.Avg))
	}

	// Right panel — T (green)
	fillRoundedRect(img, tpl.RightPanel.X, tpl.RightPanel.Y, tpl.RightPanel.W, tpl.RightPanel.H, tpl.RightPanel.R, tpl.TPanelColor)
	drawRightText(img, faces.Small, tpl.RightPanel.X+tpl.RightPanel.W-18, tpl.RightPanel.Y+34, tpl.SoftT, "Terrorists")
	tCapName := data.TCaptain
	if tCapName == "" && len(data.TPlayers) > 0 { tCapName = data.TPlayers[0].Name }
	drawRightText(img, faces.Title, tpl.RightPanel.X+tpl.RightPanel.W-18, tpl.RightPanel.Y+82, tpl.TextPrimary, trimText(faces.Title, tCapName, 320))
	tWinStr := fmt.Sprintf("%d%%", data.WinPctT)
	tEloStr := fmt.Sprintf("%d", data.TElo)
	drawRightText(img, faces.Small, tpl.RightPanel.X+tpl.RightPanel.W-170, tpl.RightPanel.Y+108, tpl.SoftT, "ELO")
	drawRightText(img, faces.BodyBold, tpl.RightPanel.X+tpl.RightPanel.W-110, tpl.RightPanel.Y+108, tpl.TAccent, tEloStr)
	drawRightText(img, faces.Small, tpl.RightPanel.X+tpl.RightPanel.W-280, tpl.RightPanel.Y+108, tpl.SoftT, "Victory %")
	drawRightText(img, faces.BodyBold, tpl.RightPanel.X+tpl.RightPanel.W-220, tpl.RightPanel.Y+108, tpl.TextPrimary, tWinStr)
	if len(data.TPlayers) > 0 { drawRoundedImageFromURL(img, data.TPlayers[0].AvatarURL, tpl.RightPanel.X+tpl.RightPanel.W-88, tpl.RightPanel.Y+44, 70, 70, 14, rgba(10, 70, 20)) }

	for idx, p := range data.TPlayers {
		y := tpl.SideCardTop + idx*tpl.SideCardGap
		if y+tpl.SideCardH > H-20 { break }
		fillRoundedRect(img, tpl.RightPanel.X+10, y, tpl.SideCardW, tpl.SideCardH, 18, rgbaA(15, 65, 20, 220))
		drawRoundedImageFromURL(img, p.AvatarURL, tpl.RightPanel.X+22, y+16, 72, 72, 14, rgba(10, 70, 20))
		drawText(img, faces.Small, tpl.RightPanel.X+108, y+34, tpl.SoftT, "Team")
		drawText(img, faces.BodyBold, tpl.RightPanel.X+108, y+62, tpl.TextPrimary, trimText(faces.BodyBold, p.Name, 230))
		drawText(img, faces.Tiny, tpl.RightPanel.X+108, y+88, rgba(130, 155, 135), fmt.Sprintf("Matches %d", p.Matches))
		drawText(img, faces.Tiny, tpl.RightPanel.X+210, y+88, rgba(130, 155, 135), fmt.Sprintf("K/D %.2f", p.KD))
		drawText(img, faces.Tiny, tpl.RightPanel.X+318, y+88, rgba(130, 155, 135), fmt.Sprintf("AVG %.0f", p.Avg))
	}

	// Center panel
	centerX := tpl.CenterPanel.X
	centerW := tpl.CenterPanel.W
	fillRoundedRect(img, tpl.CenterPanel.X, tpl.CenterPanel.Y, tpl.CenterPanel.W, tpl.CenterPanel.H, tpl.CenterPanel.R, tpl.CenterPanelColor)

	// Map header
	drawCenteredText(img, faces.Small, centerX+centerW/2, tpl.CenterHeaderY, tpl.TextMuted, "Map")
	drawCenteredText(img, faces.Title, centerX+centerW/2, tpl.CenterMapY, tpl.TextPrimary, data.MapName)

	// ELITE FPL logo text center
	drawCenteredText(img, faces.BodyBold, centerX+centerW/2, tpl.CenterBadgeY-24, tpl.CTAccent, "ELITE")
	drawCenteredText(img, faces.BodyBold, centerX+centerW/2, tpl.CenterBadgeY, tpl.TAccent, "FPL")

	// League + format
	leagueShort := strings.TrimPrefix(strings.TrimPrefix(data.Category, "🔴 | "), "🟣 | ")
	leagueShort = strings.TrimPrefix(strings.TrimPrefix(leagueShort, "🟢 | "), "⚪ | ")
	fillRoundedRect(img, tpl.MetaStrip.X, tpl.MetaStrip.Y, tpl.MetaStrip.W, tpl.MetaStrip.H, tpl.MetaStrip.R, tpl.InnerPanelColor)
	drawCenteredText(img, faces.Body, centerX+centerW/2-80, tpl.MetaStrip.Y+38, tpl.TextPrimary, leagueShort)
	drawText(img, faces.BodyBold, centerX+centerW/2+10, tpl.MetaStrip.Y+38, tpl.CTAccent, "BO")
	drawText(img, faces.BodyBold, centerX+centerW/2+45, tpl.MetaStrip.Y+38, tpl.TextPrimary, "Best of 1")

	// Host block
	fillRoundedRect(img, tpl.HostCard.X, tpl.HostCard.Y, tpl.HostCard.W, tpl.HostCard.H, tpl.HostCard.R, tpl.InnerPanelColor)
	drawRoundedImageFromURL(img, data.HostAvatar, tpl.HostCard.X+16, tpl.HostCard.Y+16, 72, 72, 14, rgba(80, 70, 90))
	drawText(img, faces.Small, tpl.HostCard.X+100, tpl.HostCard.Y+42, tpl.TextMuted, "Host")
	drawText(img, faces.SubTitle, tpl.HostCard.X+100, tpl.HostCard.Y+74, tpl.TextPrimary, trimText(faces.SubTitle, data.HostName, 180))
	hostIDStr := data.HostGameID
	if hostIDStr == "" { hostIDStr = "—" }
	drawText(img, faces.Small, tpl.HostCard.X+280, tpl.HostCard.Y+42, rgba(140, 140, 150), "ID")
	drawText(img, faces.SubTitle, tpl.HostCard.X+316, tpl.HostCard.Y+74, rgba(190, 190, 195), hostIDStr)
	drawText(img, faces.Tiny, tpl.HostCard.X+16, tpl.HostCard.Y+100, rgba(140, 140, 150), "ℹ Хост создаёт лобби в игре")

	// ELO bar
	fillRoundedRect(img, tpl.EloCard.X, tpl.EloCard.Y, tpl.EloCard.W, tpl.EloCard.H, tpl.EloCard.R, tpl.InnerPanelColor)
	barX := tpl.EloCard.X + 4
	barW := tpl.EloCard.W - 8
	drawText(img, faces.SubTitle, barX, tpl.EloCard.Y+32, tpl.CTAccent, fmt.Sprintf("%d", data.CTElo))
	drawRightText(img, faces.SubTitle, barX+barW, tpl.EloCard.Y+32, tpl.TAccent, fmt.Sprintf("%d", data.TElo))
	drawCenteredText(img, faces.Small, centerX+centerW/2, tpl.EloCard.Y+32, tpl.TextMuted, "ELO")
	barY := tpl.EloCard.Y + 44
	barH := 18
	fillRoundedRect(img, barX, barY, barW, barH, barH/2, tpl.TAccent)
	ctBarW := barW * data.WinPctCT / 100
	if ctBarW < 8 { ctBarW = 8 }
	fillRoundedRect(img, barX, barY, ctBarW, barH, barH/2, tpl.CTAccent)
	drawText(img, faces.Tiny, barX, barY+barH+18, rgba(140, 140, 150), "ℹ Играется строго эта карта")

	// Players count
	drawCenteredText(img, faces.Body, centerX+centerW/2, tpl.FooterNoteY, tpl.TextMuted, fmt.Sprintf("5vs5 · Матч #%d", data.MatchID))

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
