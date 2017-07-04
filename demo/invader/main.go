// +build darwin linux windows

package main

import (
	"encoding/binary"
	"encoding/gob"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"time"

	"github.com/udhos/goglmath"

	"golang.org/x/mobile/app"
	"golang.org/x/mobile/asset"
	"golang.org/x/mobile/event/lifecycle"
	"golang.org/x/mobile/event/mouse"
	"golang.org/x/mobile/event/paint"
	"golang.org/x/mobile/event/size"
	"golang.org/x/mobile/event/touch"
	"golang.org/x/mobile/exp/f32"
	"golang.org/x/mobile/exp/gl/glutil"
	"golang.org/x/mobile/gl"

	"github.com/udhos/fugo/msg"
)

type gameState struct {
	width      int
	height     int
	gl         gl.Context
	program    gl.Program
	buf        gl.Buffer
	position   gl.Attrib
	P          gl.Uniform // projection mat4 uniform
	proj       goglmath.Matrix4
	aspect     float64
	shaderVert string
	shaderFrag string
	serverAddr string
}

func newGame() (*gameState, error) {
	game := &gameState{}

	vert, errVert := loadFull("shader.vert")
	if errVert != nil {
		log.Printf("load vertex shader: %v", errVert)
		return nil, errVert
	}
	game.shaderVert = string(vert)

	frag, errFrag := loadFull("shader.frag")
	if errFrag != nil {
		log.Printf("load fragment shader: %v", errFrag)
		return nil, errFrag
	}
	game.shaderFrag = string(frag)

	server, errServ := loadFull("server.txt")
	if errServ != nil {
		log.Printf("load server: %v", errServ)
		return nil, errServ
	}
	game.serverAddr = strings.TrimSpace(string(server))

	log.Printf("server: [%s]", game.serverAddr)

	return game, nil
}

func main() {
	log.Print("main begin")

	slowPaint := len(os.Args) > 1
	log.Printf("slowPaint: %v", slowPaint)

	var frames int
	var paints int
	sec := time.Now().Second()
	game, errGame := newGame()
	if errGame != nil {
		log.Printf("main: fatal: %v", errGame)
		return
	}

	gob.Register(msg.Update{})

	app.Main(func(a app.App) {
		log.Print("app.Main begin")

		go serverHandler(a, game.serverAddr)

	LOOP:
		for e := range a.Events() {
			switch t := a.Filter(e).(type) {
			case lifecycle.Event:
				log.Printf("Lifecycle: %v", t)

				if t.From > t.To && t.To == lifecycle.StageDead {
					log.Printf("lifecycle down to dead")
					break LOOP
				}

				if t.Crosses(lifecycle.StageAlive) == lifecycle.CrossOff {
					log.Printf("lifecycle cross down alive")
					break LOOP
				}

				switch t.Crosses(lifecycle.StageVisible) {
				case lifecycle.CrossOn:
					glc, isGL := t.DrawContext.(gl.Context)
					if !isGL {
						log.Printf("Lifecycle: visible: bad GL context")
						continue LOOP
					}
					game.start(glc)
					a.Send(paint.Event{}) // start drawing
				case lifecycle.CrossOff:
					game.stop()
				}

			case paint.Event:
				if t.External || game.gl == nil {
					// As we are actively painting as fast as
					// we can (usually 60 FPS), skip any paint
					// events sent by the system.
					continue
				}

				paints++ // events

				if now := time.Now().Second(); now != sec {
					log.Printf("fps: %d, paints: %d", frames, paints)
					frames = 0
					paints = 0
					sec = now
				}

				if !slowPaint || frames == 0 {
					frames++ // draws
					game.paint()
					a.Publish()
				}

				if slowPaint {
					time.Sleep(500 * time.Millisecond)
				}

				// we request next paint event
				// in order to draw as fast as possible
				a.Send(paint.Event{})
			case mouse.Event:
				game.input(t.X, t.Y)
			case touch.Event:
				game.input(t.X, t.Y)
			case size.Event:
				game.resize(t.WidthPx, t.HeightPx)
			case msg.Update:
				log.Printf("app.Main event update: %v", t)
			}
		}

		log.Print("app.Main end")
	})

	log.Print("main end")
}

func loadFull(name string) ([]byte, error) {
	f, errOpen := asset.Open(name)
	if errOpen != nil {
		return nil, errOpen
	}
	defer f.Close()
	buf, errRead := ioutil.ReadAll(f)
	if errRead != nil {
		return nil, errRead
	}
	log.Printf("loaded: %s (%d bytes)", name, len(buf))
	return buf, nil
}

func (game *gameState) resize(w, h int) {
	if game.width != w || game.height != h {
		log.Printf("resize: %d,%d", w, h)
	}
	game.width = w
	game.height = h

	glc := game.gl // shortcut
	if glc == nil {
		return
	}

	glc.Viewport(0, 0, w, h)

	if h >= w {
		game.aspect = float64(h) / float64(w)
		goglmath.SetOrthoMatrix(&game.proj, -1, 1, -game.aspect, game.aspect, -1, 1)
		return
	}

	game.aspect = float64(w) / float64(h)
	goglmath.SetOrthoMatrix(&game.proj, -game.aspect, game.aspect, -1, 1, -1, 1)
}

func (game *gameState) input(x, y float32) {
	log.Printf("input: %f,%f (%d x %d)", x, y, game.width, game.height)
}

func (game *gameState) start(glc gl.Context) {
	log.Printf("start")

	var err error
	game.program, err = glutil.CreateProgram(glc, game.shaderVert, game.shaderFrag)
	if err != nil {
		log.Printf("start: error creating GL program: %v", err)
		return
	}

	log.Printf("start: shader compiled")

	game.buf = glc.CreateBuffer()
	glc.BindBuffer(gl.ARRAY_BUFFER, game.buf)
	glc.BufferData(gl.ARRAY_BUFFER, triangleData, gl.STATIC_DRAW)

	game.position = glc.GetAttribLocation(game.program, "position")
	game.P = glc.GetUniformLocation(game.program, "P")

	glc.ClearColor(.5, .5, .5, 1) // gray background

	game.gl = glc

	log.Printf("start: shader initialized")
}

func (game *gameState) stop() {
	log.Printf("stop")

	glc := game.gl // shortcut

	glc.DeleteProgram(game.program)
	glc.DeleteBuffer(game.buf)

	game.gl = nil

	log.Printf("stop: shader disposed")
}

func (game *gameState) paint() {
	glc := game.gl // shortcut

	glc.Clear(gl.COLOR_BUFFER_BIT) // draw ClearColor background

	glc.UseProgram(game.program)
	glc.EnableVertexAttribArray(game.position)

	glc.UniformMatrix4fv(game.P, game.proj.Data())

	glc.BindBuffer(gl.ARRAY_BUFFER, game.buf)

	// how to get data for location attribute within buffer
	glc.VertexAttribPointer(game.position, coordsPerVertex, gl.FLOAT, false, 0, 0)

	glc.DrawArrays(gl.TRIANGLES, 0, vertexCount)

	glc.DisableVertexAttribArray(game.position)
}

const (
	coordsPerVertex = 3
	vertexCount     = 3
)

var triangleData = f32.Bytes(binary.LittleEndian,
	0.0, 1.0, 0.0, // top left
	0.0, 0.0, 0.0, // bottom left
	1.0, 0.0, 0.0, // bottom right
)