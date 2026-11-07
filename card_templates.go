package main

import "image/color"

type CardRect struct {
	X int
	Y int
	W int
	H int
	R int
}

type MatchCardTemplate struct {
	CanvasW          int
	CanvasH          int
	Frame            CardRect
	Header           CardRect
	HeaderLeftIcon   CardRect
	HeaderRightIcon  CardRect
	MatchIDX         int
	MatchIDBaseline  int
	LogoRight        int
	LogoBaseline     int
	ScoreTitleY      int
	ScoreValueY      int
	LeftTableX       int
	RightTableX      int
	TableHeaderY     int
	TableRowOffsetY  int
	TableRowH        int
	PlayerW          int
	StatW            int
	Gap              int
	BottomLeftTitleY int
	BottomLeftCard   CardRect
	BottomRightCard  CardRect
	MVPAvatar        CardRect
	MVPStat1         CardRect
	MVPStat2         CardRect
	MVPStat3         CardRect
	BackgroundLeft   color.RGBA
	BackgroundRight  color.RGBA
	FrameColor       color.RGBA
	HeaderLeft       color.RGBA
	HeaderRight      color.RGBA
	PanelColor       color.RGBA
	TextPrimary      color.RGBA
	TextMuted        color.RGBA
	AccentCT         color.RGBA
	AccentT          color.RGBA
	AccentHighlight  color.RGBA
}

type ProfileCardTemplate struct {
	CanvasW            int
	CanvasH            int
	Banner             CardRect
	BannerAvatar       CardRect
	BannerNameX        int
	BannerNameY        int
	BannerMetaY        int
	BannerGameIDX      int
	TopIconY           int
	TopIconSize        int
	StatPanel          CardRect
	InfoPanel          CardRect
	LeaguePanel        CardRect
	MapsPanel          CardRect
	RecentPanel        CardRect
	MainStatCard       CardRect
	KDCard             CardRect
	HistoryCard        CardRect
	ImpactCard         CardRect
	MapOverviewCard    CardRect
	BestMapCard        CardRect
	MapGridX           int
	MapGridY           int
	MapGridW           int
	MapGridH           int
	MapGridGapX        int
	MapGridGapY        int
	RecentGridX        int
	RecentGridY        int
	RecentCell         int
	RecentGap          int
	RecentCols         int
	RecentRows         int
	BackgroundLeft     color.RGBA
	BackgroundRight    color.RGBA
	PanelColor         color.RGBA
	PanelGreen         color.RGBA
	PanelDark          color.RGBA
	TextPrimary        color.RGBA
	TextMuted          color.RGBA
	AccentPurple       color.RGBA
	AccentGreen        color.RGBA
	AccentRed          color.RGBA
	AccentLine         color.RGBA
}

type PickCardTemplate struct {
	CanvasW           int
	CanvasH           int
	LeftPanel         CardRect
	CenterPanel       CardRect
	RightPanel        CardRect
	SideCardXPad      int
	SideCardTop       int
	SideCardW         int
	SideCardH         int
	SideCardGap       int
	CenterHeaderY     int
	CenterMapY        int
	CenterBadgeY      int
	MetaStrip         CardRect
	HostCard          CardRect
	EloCard           CardRect
	FooterNoteY       int
	BackgroundLeft    color.RGBA
	BackgroundRight   color.RGBA
	CTPanelColor      color.RGBA
	TPanelColor       color.RGBA
	CenterPanelColor  color.RGBA
	InnerPanelColor   color.RGBA
	TextPrimary       color.RGBA
	TextMuted         color.RGBA
	CTAccent          color.RGBA
	TAccent           color.RGBA
	SoftCT            color.RGBA
	SoftT             color.RGBA
}

var DefaultMatchCardTemplate = MatchCardTemplate{
	CanvasW:          1600,
	CanvasH:          900,
	Frame:            CardRect{X: 28, Y: 24, W: 1544, H: 852, R: 34},
	Header:           CardRect{X: 46, Y: 132, W: 1508, H: 92, R: 26},
	HeaderLeftIcon:   CardRect{X: 58, Y: 144, W: 74, H: 68, R: 20},
	HeaderRightIcon:  CardRect{X: 1468, Y: 144, W: 74, H: 68, R: 20},
	MatchIDX:         58,
	MatchIDBaseline:  98,
	LogoRight:        1548,
	LogoBaseline:     74,
	ScoreTitleY:      160,
	ScoreValueY:      208,
	LeftTableX:       48,
	RightTableX:      815,
	TableHeaderY:     266,
	TableRowOffsetY:  22,
	TableRowH:        78,
	PlayerW:          390,
	StatW:            64,
	Gap:              8,
	BottomLeftTitleY: 708,
	BottomLeftCard:   CardRect{X: 58, Y: 728, W: 730, H: 150, R: 24},
	BottomRightCard:  CardRect{X: 850, Y: 728, W: 702, H: 150, R: 24},
	MVPAvatar:        CardRect{X: 864, Y: 742, W: 140, H: 120, R: 18},
	MVPStat1:         CardRect{X: 1260, Y: 742, W: 96, H: 120, R: 20},
	MVPStat2:         CardRect{X: 1378, Y: 742, W: 96, H: 120, R: 20},
	MVPStat3:         CardRect{X: 1496, Y: 742, W: 96, H: 120, R: 20},
	BackgroundLeft:   rgba(38, 8, 52),
	BackgroundRight:  rgba(17, 62, 8),
	FrameColor:       rgbaA(20, 17, 25, 235),
	HeaderLeft:       rgba(116, 42, 140),
	HeaderRight:      rgba(31, 108, 19),
	PanelColor:       rgba(39, 35, 46),
	TextPrimary:      rgba(245, 245, 245),
	TextMuted:        rgba(190, 190, 195),
	AccentCT:         rgba(72, 23, 116),
	AccentT:          rgba(18, 90, 20),
	AccentHighlight:  rgba(74, 230, 156),
}

var DefaultProfileCardTemplate = ProfileCardTemplate{
	CanvasW:         1600,
	CanvasH:         1800,
	Banner:          CardRect{X: 70, Y: 40, W: 1460, H: 220, R: 38},
	BannerAvatar:    CardRect{X: 106, Y: 78, W: 200, H: 200, R: 28},
	BannerNameX:     340,
	BannerNameY:     174,
	BannerMetaY:     214,
	BannerGameIDX:   480,
	TopIconY:        120,
	TopIconSize:     56,
	StatPanel:       CardRect{X: 70, Y: 330, W: 960, H: 690, R: 34},
	InfoPanel:       CardRect{X: 1060, Y: 330, W: 470, H: 250, R: 32},
	LeaguePanel:     CardRect{X: 1060, Y: 600, W: 470, H: 500, R: 32},
	MapsPanel:       CardRect{X: 70, Y: 1060, W: 960, H: 660, R: 34},
	RecentPanel:     CardRect{X: 1060, Y: 1150, W: 470, H: 570, R: 34},
	MainStatCard:    CardRect{X: 120, Y: 426, W: 455, H: 185, R: 24},
	KDCard:          CardRect{X: 120, Y: 630, W: 455, H: 245, R: 24},
	HistoryCard:     CardRect{X: 595, Y: 426, W: 405, H: 350, R: 24},
	ImpactCard:      CardRect{X: 595, Y: 796, W: 405, H: 100, R: 24},
	MapOverviewCard: CardRect{X: 100, Y: 1180, W: 315, H: 260, R: 20},
	BestMapCard:     CardRect{X: 100, Y: 1460, W: 315, H: 220, R: 20},
	MapGridX:        430,
	MapGridY:        1180,
	MapGridW:        170,
	MapGridH:        220,
	MapGridGapX:     185,
	MapGridGapY:     240,
	RecentGridX:     1096,
	RecentGridY:     1292,
	RecentCell:      64,
	RecentGap:       78,
	RecentCols:      5,
	RecentRows:      5,
	BackgroundLeft:  rgba(54, 12, 74),
	BackgroundRight: rgba(17, 79, 11),
	PanelColor:      rgbaA(35, 33, 37, 235),
	PanelGreen:      rgbaA(30, 36, 25, 235),
	PanelDark:       rgba(52, 50, 55),
	TextPrimary:     rgba(245, 245, 245),
	TextMuted:       rgba(155, 155, 166),
	AccentPurple:    rgba(195, 71, 255),
	AccentGreen:     rgba(55, 223, 146),
	AccentRed:       rgba(255, 88, 116),
	AccentLine:      rgba(110, 110, 110),
}

var DefaultPickCardTemplate = PickCardTemplate{
	CanvasW:          1600,
	CanvasH:          900,
	LeftPanel:        CardRect{X: 10, Y: 10, W: 490, H: 880, R: 24},
	CenterPanel:      CardRect{X: 505, Y: 10, W: 595, H: 880, R: 24},
	RightPanel:       CardRect{X: 1110, Y: 10, W: 480, H: 880, R: 24},
	SideCardXPad:     20,
	SideCardTop:      165,
	SideCardW:        470,
	SideCardH:        108,
	SideCardGap:      120,
	CenterHeaderY:    48,
	CenterMapY:       86,
	CenterBadgeY:     152,
	MetaStrip:        CardRect{X: 525, Y: 180, W: 555, H: 60, R: 18},
	HostCard:         CardRect{X: 525, Y: 258, W: 555, H: 110, R: 18},
	EloCard:          CardRect{X: 525, Y: 388, W: 555, H: 90, R: 18},
	FooterNoteY:      488,
	BackgroundLeft:   rgba(42, 8, 60),
	BackgroundRight:  rgba(8, 55, 10),
	CTPanelColor:     rgbaA(70, 20, 90, 230),
	TPanelColor:      rgbaA(10, 60, 15, 230),
	CenterPanelColor: rgbaA(28, 28, 32, 240),
	InnerPanelColor:  rgba(44, 44, 50),
	TextPrimary:      rgba(245, 245, 245),
	TextMuted:        rgba(170, 170, 180),
	CTAccent:         rgba(195, 71, 255),
	TAccent:          rgba(55, 200, 100),
	SoftCT:           rgba(190, 180, 200),
	SoftT:            rgba(170, 200, 170),
}
