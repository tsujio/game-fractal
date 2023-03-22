package main

import (
	"embed"
	"fmt"
	"image"
	"image/color"
	"log"
	"math"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/audio"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/text"
	"github.com/hajimehoshi/ebiten/v2/vector"
	"github.com/samber/lo"
	logging "github.com/tsujio/game-logging-server/client"
	"github.com/tsujio/game-util/drawutil"
	"github.com/tsujio/game-util/loggingutil"
	"github.com/tsujio/game-util/mathutil"
	"github.com/tsujio/game-util/resourceutil"
	"github.com/tsujio/game-util/touchutil"
)

const (
	gameName     = "fractal"
	screenWidth  = 640
	screenHeight = 480
	coinHitZ     = 180
	playerHitR   = 5.0
)

//go:embed resources/*.ttf resources/*.dat resources/bgm-*.wav resources/secret
var resources embed.FS

var (
	fontL, fontM, fontS = resourceutil.ForceLoadFont(resources, "resources/PressStart2P-Regular.ttf", nil)
	audioContext        = audio.NewContext(48000)
	gameStartAudioData  = resourceutil.ForceLoadDecodedAudio(resources, "resources/魔王魂 効果音 システム49.mp3.dat", audioContext)
	gameOverAudioData   = resourceutil.ForceLoadDecodedAudio(resources, "resources/魔王魂 効果音 システム32.mp3.dat", audioContext)
	scoreUpAudioData    = resourceutil.ForceLoadDecodedAudio(resources, "resources/魔王魂 効果音 物音15.mp3.dat", audioContext)
	bgmPlayer           = resourceutil.ForceCreateBGMPlayer(resources, "resources/bgm-fractal.wav", audioContext)
)

var (
	emptyImage = func() *ebiten.Image {
		img := ebiten.NewImage(3, 3)
		img.Fill(color.White)
		return img
	}()
	emptySubImage = emptyImage.SubImage(image.Rect(1, 1, 2, 2)).(*ebiten.Image)
)

func expandLine(s, t *mathutil.Vector2D) []*mathutil.Vector2D {
	p1 := t.Sub(s).Div(3.0).Add(s)
	p2 := t.Sub(s).Div(3.0).Mul(2.0).Add(s)
	p3 := p2.Sub(p1).Rotate(-math.Pi / 3).Add(p1)
	return []*mathutil.Vector2D{s, p1, p3, p2, t}
}

func zoom(p *mathutil.Vector2D, c *mathutil.Vector2D, s float64) *mathutil.Vector2D {
	return p.Sub(c).Mul(s).Add(c)
}

type Coin struct {
	*mathutil.Vector3D
	vr  float64
	hit bool
}

var coinImage = drawutil.CreatePatternImage([][]rune{
	[]rune("  ##  "),
	[]rune(" #.## "),
	[]rune("#.####"),
	[]rune("####/#"),
	[]rune("####/#"),
	[]rune("####/#"),
	[]rune("####/#"),
	[]rune(" ##/# "),
	[]rune("  ##  "),
}, &drawutil.CreatePatternImageOption[rune]{
	ColorMap: map[rune]color.Color{
		'#': color.RGBA{0xff, 0xe0, 0, 0xff},
		'/': color.RGBA{0xf5, 0xc0, 0, 0xff},
		'.': color.RGBA{0xff, 0xf0, 0, 0xff},
	},
})

func (c *Coin) draw(screen *ebiten.Image) {
	_, h := coinImage.Size()
	opts := &ebiten.DrawImageOptions{}
	opts.GeoM.Scale(c.vr*2/float64(h), c.vr*2/float64(h))

	if c.Z > coinHitZ {
		opts.ColorM.Translate(-0.3, -0.3, -0.3, 0)
	}

	drawutil.DrawImageAt(screen, coinImage, c.X, c.Y, opts)
}

type CoinHitEffect struct {
	*mathutil.Vector2D
	ticks uint
	gain  int
}

func (e *CoinHitEffect) draw(screen *ebiten.Image) {
	x := e.X
	y := e.Y - 10.0*math.Sin(float64(e.ticks)*math.Pi/60)
	text.Draw(screen, fmt.Sprintf("%+d", e.gain), fontM.Face, int(x), int(y), color.RGBA{0xff, 0xff, 0, 0xff})
}

type Player struct {
	*mathutil.Vector2D
	v    *mathutil.Vector2D
	r    float64
	life float64
}

var playerImages = drawutil.CreatePatternImageArray([][][]rune{
	{
		[]rune(" ######## "),
		[]rune("##########"),
		[]rune("########.#"),
		[]rune("########.#"),
		[]rune("########.#"),
		[]rune("##########"),
		[]rune("##########"),
		[]rune("######### "),
		[]rune("########  "),
		[]rune("#######   "),
	},
	{
		[]rune("  ######  "),
		[]rune(" ######## "),
		[]rune("##########"),
		[]rune("##########"),
		[]rune("##########"),
		[]rune("##########"),
		[]rune("##########"),
		[]rune("##.####.##"),
		[]rune("##.####.##"),
		[]rune(" ######## "),
	},
}, &drawutil.CreatePatternImageOption[rune]{
	ColorMap: map[rune]color.Color{
		'#': color.RGBA{0xF5, 0xB4, 0xEA, 0xff},
		'.': color.RGBA{0, 0, 0, 0xff},
	},
	DotSize: 1.5,
})

func (p *Player) draw(screen *ebiten.Image) {
	opts := &ebiten.DrawImageOptions{}

	t := math.Atan2(p.v.Y, p.v.X)
	sin, cos := math.Sin(t), math.Cos(t)

	var img *ebiten.Image
	if math.Abs(cos) < 1e-1 {
		img = playerImages[1]
		if sin < 0 {
			opts.GeoM.Scale(1, -1)
		}
	} else {
		img = playerImages[0]
		if cos < 0 {
			opts.GeoM.Scale(-1, 1)
		}
	}

	drawutil.DrawImageAt(screen, img, p.X, p.Y, opts)
}

type GameMode int

const (
	GameModeTitle GameMode = iota
	GameModePlaying
	GameModeGameOver
)

type Game struct {
	playerID              string
	playID                string
	fixedRandomSeed       int64
	touchContext          *touchutil.TouchContext
	random                *rand.Rand
	mode                  GameMode
	ticksFromModeStart    uint64
	score                 int
	points                []*mathutil.Vector2D
	player                *Player
	coins                 []*Coin
	coinHitEffects        []*CoinHitEffect
	zoomScale             float64
	ticksFromAllPointsOut uint64
}

func (g *Game) Update() error {
	g.touchContext.Update()

	g.ticksFromModeStart++

	loggingutil.SendTouchLog(gameName, g.playerID, g.playID, g.ticksFromModeStart, g.touchContext)

	switch g.mode {
	case GameModeTitle:
		if g.touchContext.IsJustTouched() {
			g.setNextMode(GameModePlaying)

			loggingutil.SendLog(gameName, g.playerID, g.playID, map[string]interface{}{
				"action": "start_game",
			})

			audio.NewPlayerFromBytes(audioContext, gameStartAudioData).Play()

			bgmPlayer.Rewind()
			bgmPlayer.Play()
		}
	case GameModePlaying:
		if g.ticksFromModeStart%600 == 0 {
			loggingutil.SendLog(gameName, g.playerID, g.playID, map[string]interface{}{
				"action": "playing",
				"ticks":  g.ticksFromModeStart,
				"score":  g.score,
			})
		}

		if g.ticksFromAllPointsOut > 0 {
			g.ticksFromAllPointsOut++
		}

		g.zoomScale *= 1.00002
		if g.ticksFromModeStart < 60*60 {
			g.zoomScale = math.Min(g.zoomScale, 1.010)
		} else if g.ticksFromModeStart < 75*60 {
			g.zoomScale = math.Min(g.zoomScale, 1.015)
		} else if g.ticksFromModeStart < 90*60 {
			g.zoomScale = math.Min(g.zoomScale, 1.020)
		} else {
			g.zoomScale = math.Min(g.zoomScale, 1.03)
		}

		var rate int
		if g.zoomScale < 1.015 {
			rate = 20
		} else if g.zoomScale < 1.020 {
			rate = 10
		} else if g.zoomScale < 1.03 {
			rate = 5
		} else {
			rate = 2
		}
		if g.random.Int()%rate == 0 {
			var p *mathutil.Vector2D
			for i := 0; i < 100; i++ {
				q := lo.Sample(g.points)
				distance := q.Sub(g.player.Vector2D).Norm()
				if p == nil || p.Sub(g.player.Vector2D).Norm() > distance {
					p = q
					if distance < 50 {
						break
					}
				}
			}
			coin := &Coin{
				Vector3D: &mathutil.Vector3D{
					X: p.X,
					Y: p.Y,
					Z: 0.000001,
				},
				vr: 0,
			}
			g.coins = append(g.coins, coin)
		}

		if g.player.v.Norm() < 2.0 {
			g.player.v = g.player.v.Mul(1.02)
		}
		if g.player.v.Norm() > 2.0 {
			g.player.v = g.player.v.Normalize().Mul(2.0)
		}

		if len(g.points) < 500 {
			var newPoints []*mathutil.Vector2D
			for i := 0; i < len(g.points)-1; i++ {
				s, t := g.points[i], g.points[i+1]
				points := expandLine(s, t)
				if i > 0 {
					points = points[1:]
				}
				newPoints = append(newPoints, points...)
			}
			g.points = newPoints
		}

		if g.touchContext.IsJustTouched() {
			g.player.v = g.player.v.Rotate(math.Pi / 2)
		}

		g.player.Vector2D = g.player.Vector2D.Add(g.player.v)
		if g.player.Vector2D.X < 0 {
			g.player.Vector2D.X = 0
		}
		if g.player.Vector2D.X > screenWidth {
			g.player.Vector2D.X = screenWidth
		}
		if g.player.Vector2D.Y < 0 {
			g.player.Vector2D.Y = 0
		}
		if g.player.Vector2D.Y > screenHeight {
			g.player.Vector2D.Y = screenHeight
		}

		g.player.life -= 0.2

		if g.ticksFromAllPointsOut == 0 {
			g.points = lo.Map(g.points, func(p *mathutil.Vector2D, i int) *mathutil.Vector2D {
				return zoom(p, g.player.Vector2D, g.zoomScale)
			})
		}

		g.coins = lo.Map(g.coins, func(c *Coin, i int) *Coin {
			pos := &mathutil.Vector2D{X: c.X, Y: c.Y}
			pos = zoom(pos, g.player.Vector2D, g.zoomScale)
			c.X, c.Y = pos.X, pos.Y
			c.Z += 1.0 * g.zoomScale
			c.vr = 10.0 * c.Z / coinHitZ
			return c
		})

		g.coinHitEffects = lo.Map(g.coinHitEffects, func(e *CoinHitEffect, i int) *CoinHitEffect {
			e.ticks++
			return e
		})

		for _, c := range g.coins {
			if c.vr > 1.0 &&
				math.Pow(c.X-g.player.X, 2)+math.Pow(c.Y-g.player.Y, 2) < math.Pow(c.vr+g.player.r, 2) {
				c.hit = true

				gain := lo.If(c.Z < coinHitZ, 1000).Else(200)

				g.score += gain

				effect := &CoinHitEffect{
					Vector2D: &mathutil.Vector2D{
						X: c.X,
						Y: c.Y,
					},
					gain: gain,
				}
				g.coinHitEffects = append(g.coinHitEffects, effect)

				g.player.life += 50
				if g.player.life > 100.0 {
					g.player.life = 100.0
				}

				audio.NewPlayerFromBytes(audioContext, scoreUpAudioData).Play()
			}
		}

		for i := 0; i < len(g.points); i++ {
			p := g.points[i]
			if p.X > -50 && p.X < screenWidth+50 && p.Y > -50 && p.Y < screenHeight+50 {
				g.points = g.points[i:]
				break
			}

			if g.ticksFromAllPointsOut == 0 && i == len(g.points)-1 {
				g.ticksFromAllPointsOut++
			}
		}
		for i := len(g.points) - 1; i >= 0; i-- {
			p := g.points[i]
			if p.X > -50 && p.X < screenWidth+50 && p.Y > -50 && p.Y < screenHeight+50 {
				g.points = g.points[:i+1]
				break
			}
		}

		g.coins = lo.Filter(g.coins, func(c *Coin, i int) bool {
			return !c.hit &&
				c.X > -50 && c.X < screenWidth+50 &&
				c.Y > -50 && c.Y < screenHeight+50 &&
				c.Z < 1000
		})

		g.coinHitEffects = lo.Filter(g.coinHitEffects, func(e *CoinHitEffect, i int) bool {
			return e.ticks < 60
		})

		if g.player.life <= 0 || g.ticksFromAllPointsOut > 160 {
			loggingutil.SendLog(gameName, g.playerID, g.playID, map[string]interface{}{
				"action": "game_over",
				"score":  g.score,
			})

			g.setNextMode(GameModeGameOver)

			loggingutil.RegisterScoreToRankingAsync(gameName, g.playerID, g.playID, g.score)

			audio.NewPlayerFromBytes(audioContext, gameOverAudioData).Play()
		}
	case GameModeGameOver:
		if g.ticksFromModeStart > 60 && g.touchContext.IsJustTouched() {
			g.initialize()
			bgmPlayer.Pause()
		}
	}

	return nil
}

func (g *Game) drawFractal(screen *ebiten.Image) {
	var path vector.Path
	for i, p := range g.points {
		if i == 0 {
			path.MoveTo(float32(p.X), float32(p.Y))
		} else {
			path.LineTo(float32(p.X), float32(p.Y))
		}
	}

	s, t := g.points[0], g.points[len(g.points)-1]
	st := t.Sub(s).Normalize()
	p := t.Add(st.Mul(2000))
	path.LineTo(float32(p.X), float32(p.Y))
	p = p.Add(st.Rotate(math.Pi / 2).Mul(2000))
	path.LineTo(float32(p.X), float32(p.Y))
	p = p.Add(st.Rotate(math.Pi).Mul(2000))
	path.LineTo(float32(p.X), float32(p.Y))
	p = s.Add(st.Mul(-2000))
	path.LineTo(float32(p.X), float32(p.Y))

	op := &ebiten.DrawTrianglesOptions{
		FillRule: ebiten.EvenOdd,
	}
	vs, is := path.AppendVerticesAndIndicesForFilling(nil, nil)
	for i := range vs {
		vs[i].SrcX = 1
		vs[i].SrcY = 1
		vs[i].ColorR = 0x33 / float32(0xff)
		vs[i].ColorG = 0xcc / float32(0xff)
		vs[i].ColorB = 0x66 / float32(0xff)
	}
	screen.DrawTriangles(vs, is, emptySubImage, op)
}

func (g *Game) drawTitleText(screen *ebiten.Image) {
	titleTexts := []string{"FRACTAL"}
	for i, s := range titleTexts {
		text.Draw(screen, s, fontL.Face, screenWidth/2-len(s)*int(fontL.FaceOptions.Size)/2, 110+i*int(fontL.FaceOptions.Size*1.8), color.White)
	}

	usageTexts := []string{"[TAP] Change character direction"}
	for i, s := range usageTexts {
		text.Draw(screen, s, fontS.Face, screenWidth/2-len(s)*int(fontS.FaceOptions.Size)/2, 200+i*int(fontS.FaceOptions.Size*1.8), color.White)
	}

	creditTexts := []string{"CREATOR: NAOKI TSUJIO", "FONT: Press Start 2P by CodeMan38", "SOUND EFFECT: MaouDamashii"}
	for i, s := range creditTexts {
		text.Draw(screen, s, fontS.Face, screenWidth/2-len(s)*int(fontS.FaceOptions.Size)/2, 420+i*int(fontS.FaceOptions.Size*1.8), color.White)
	}
}

func (g *Game) drawHole(screen *ebiten.Image) {
	if g.ticksFromAllPointsOut > 15 {
		r := 0.03 * math.Pow(float64(g.ticksFromAllPointsOut-15), 2)
		ebitenutil.DrawCircle(screen, screenWidth/2, screenHeight/2, r, color.Black)
	}
}

func (g *Game) drawScore(screen *ebiten.Image) {
	t := fmt.Sprintf("%d", g.score)
	text.Draw(screen, t, fontS.Face, screenWidth-len(t)*int(fontS.FaceOptions.Size)-10, 25, color.White)
}

func (g *Game) drawLife(screen *ebiten.Image) {
	var path vector.Path

	const r = 40.0

	path.MoveTo(float32(g.player.X), float32(g.player.Y-r))
	path.Arc(float32(g.player.X), float32(g.player.Y), float32(r), -math.Pi/2, float32(-math.Pi/2+2*math.Pi*g.player.life/100), vector.Clockwise)

	op := &vector.StrokeOptions{}
	op.Width = 5
	op.LineJoin = vector.LineJoinRound
	vs, is := path.AppendVerticesAndIndicesForStroke(nil, nil, op)

	for i := range vs {
		vs[i].SrcX = 1
		vs[i].SrcY = 1
		vs[i].ColorR = 1
		vs[i].ColorG = 1
		vs[i].ColorB = 1
		vs[i].ColorA = 0.5
	}

	opts := &ebiten.DrawTrianglesOptions{}
	screen.DrawTriangles(vs, is, emptySubImage, opts)
}

func (g *Game) drawGameOverText(screen *ebiten.Image) {
	gameOverTexts := []string{"GAME OVER"}
	for i, s := range gameOverTexts {
		text.Draw(screen, s, fontL.Face, screenWidth/2-len(s)*int(fontL.FaceOptions.Size)/2, 170+i*int(fontL.FaceOptions.Size*1.8), color.White)
	}

	scoreText := []string{"YOUR SCORE IS", fmt.Sprintf("%d!", g.score)}
	for i, s := range scoreText {
		text.Draw(screen, s, fontM.Face, screenWidth/2-len(s)*int(fontM.FaceOptions.Size)/2, 230+i*int(fontM.FaceOptions.Size*1.8), color.White)
	}
}

func (g *Game) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{0x02, 0x60, 0xF9, 0xff})

	switch g.mode {
	case GameModeTitle:
		g.drawFractal(screen)

		g.player.draw(screen)

		g.drawTitleText(screen)
	case GameModePlaying:
		g.drawFractal(screen)

		g.drawHole(screen)

		for _, c := range g.coins {
			if c.Z < coinHitZ {
				c.draw(screen)
			}
		}

		g.player.draw(screen)

		for _, c := range g.coins {
			if c.Z >= coinHitZ {
				c.draw(screen)
			}
		}

		for _, e := range g.coinHitEffects {
			e.draw(screen)
		}

		g.drawScore(screen)

		g.drawLife(screen)
	case GameModeGameOver:
		g.drawFractal(screen)

		g.drawHole(screen)

		for _, c := range g.coins {
			if c.Z < coinHitZ {
				c.draw(screen)
			}
		}

		g.player.draw(screen)

		for _, c := range g.coins {
			if c.Z >= coinHitZ {
				c.draw(screen)
			}
		}

		for _, e := range g.coinHitEffects {
			e.draw(screen)
		}

		g.drawScore(screen)

		g.drawLife(screen)

		g.drawGameOverText(screen)
	}
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (int, int) {
	return screenWidth, screenHeight
}

func (g *Game) setNextMode(mode GameMode) {
	g.mode = mode
	g.ticksFromModeStart = 0
}

func (g *Game) initialize() {
	var playID string
	if playIDObj, err := uuid.NewRandom(); err == nil {
		playID = playIDObj.String()
	}
	g.playID = playID

	var seed int64
	if g.fixedRandomSeed != 0 {
		seed = g.fixedRandomSeed
	} else {
		seed = time.Now().Unix()
	}

	loggingutil.SendLog(gameName, g.playerID, g.playID, map[string]interface{}{
		"action": "initialize",
		"seed":   seed,
	})

	g.random = rand.New(rand.NewSource(seed))
	g.score = 0
	g.points = nil
	g.player = &Player{
		Vector2D: &mathutil.Vector2D{
			X: screenWidth / 2,
			Y: screenHeight/2 + 10,
		},
		v: &mathutil.Vector2D{
			X: 0,
			Y: 0.01,
		},
		r:    playerHitR,
		life: 100.0,
	}
	g.coins = nil
	g.coinHitEffects = nil
	g.zoomScale = 1.0
	g.ticksFromAllPointsOut = 0

	p1 := &mathutil.Vector2D{X: 50, Y: screenHeight - 100}
	p2 := &mathutil.Vector2D{X: screenWidth - 50, Y: screenHeight - 100}
	p0 := p2.Sub(p1).Rotate(math.Pi / 3).Normalize().Mul(150).Add(p1)
	p3 := p1.Sub(p2).Rotate(-math.Pi / 3).Normalize().Mul(150).Add(p2)
	g.points = []*mathutil.Vector2D{p0, p1, p2, p3}

	lo.Times(5, func(_ int) any {
		var newPoints []*mathutil.Vector2D
		for i := 0; i < len(g.points)-1; i++ {
			s, t := g.points[i], g.points[i+1]
			points := expandLine(s, t)
			if i > 0 {
				points = points[1:]
			}
			newPoints = append(newPoints, points...)
		}
		g.points = newPoints

		return nil
	})

	g.setNextMode(GameModeTitle)
}

func main() {
	if os.Getenv("GAME_LOGGING") == "1" {
		secret, err := resources.ReadFile("resources/secret")
		if err == nil {
			logging.Enable(string(secret))
		}
	} else {
		logging.Disable()
	}

	var randomSeed int64
	if seed, err := strconv.Atoi(os.Getenv("GAME_RAND_SEED")); err == nil {
		randomSeed = int64(seed)
	}

	playerID := os.Getenv("GAME_PLAYER_ID")
	if playerID == "" {
		if playerIDObj, err := uuid.NewRandom(); err == nil {
			playerID = playerIDObj.String()
		}
	}

	ebiten.SetWindowSize(screenWidth, screenHeight)
	ebiten.SetWindowTitle("Fractal")

	game := &Game{
		playerID:        playerID,
		fixedRandomSeed: randomSeed,
		touchContext:    touchutil.CreateTouchContext(),
	}
	game.initialize()

	if err := ebiten.RunGame(game); err != nil {
		log.Fatal(err)
	}
}
