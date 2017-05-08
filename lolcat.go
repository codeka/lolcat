package main

import (
	"bufio"
	"container/ring"
	"os/exec"

	"github.com/mattn/go-runewidth"
	"github.com/nsf/termbox-go"
)

// BufferLineCount is the number of lines of buffer to keep in memory from logcat.
const BufferLineCount = 1000

// Device is all the stuff we know about a single attached device.
type Device struct {
	logcat *ring.Ring
}

func (d *Device) appendLine(line string) {
	d.logcat = d.logcat.Prev()
	d.logcat.Value = line
	// TODO: notify listeners
}

// Open opens a connection to the given device via an adb command. Basically we start streaming
// logcat output to the device's AbdContext.
func (d *Device) Open() {
	d.logcat = ring.New(BufferLineCount)

	cmd := exec.Command("adb", "logcat")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic("An error occurred reading output: " + err.Error())
	}
	scanner := bufio.NewScanner(stdout)
	go func() {
		for scanner.Scan() {
			d.appendLine(scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			panic("An error occurred reading output: " + err.Error())
		}
	}()
	err = cmd.Start()
	if err != nil {
		panic("Error starting adb logcat: " + err.Error())
	}
}

var d *Device

func tbprint(x, y int, fg, bg termbox.Attribute, msg string) int {
	n := 0
	for _, c := range msg {
		termbox.SetCell(x, y, c, fg, bg)
		width := runewidth.RuneWidth(c)
		x += width
		n += width
	}
	return n
}

func render() {
	coldef := termbox.ColorDefault
	termbox.Clear(coldef, coldef)
	w, h := termbox.Size()

	// Top line, device list
	x := 0
	coldef = termbox.ColorDefault | termbox.AttrReverse
	// TODO: for devices:
	x += tbprint(x, 0, coldef, coldef, "［")
	coldef = termbox.ColorDefault
	x += tbprint(x, 0, coldef, coldef, "Device Here")
	coldef = termbox.ColorDefault | termbox.AttrReverse
	x += tbprint(x, 0, coldef, coldef, "］")
	for ; x < w; x++ {
		termbox.SetCell(x, 0, ' ', coldef, coldef)
	}

	// Start from bottom and write up
	logcat := d.logcat
	coldef = termbox.ColorDefault
	for y := h - 3; y >= 1; y-- {
		if logcat.Value == nil {
			break
		}
		tbprint(0, y, coldef, coldef, logcat.Value.(string))
		logcat = logcat.Next()
	}

	// Second from bottom line, filter.
	// TODO: the first tab ("no filter") should have no filter line
	y := h - 2
	termbox.SetCursor(1, y)

	// Last line, tabs, one tab per configured filter
	x = 0
	y = h - 1
	// TODO: for tabs:
	coldef = termbox.ColorDefault
	x += tbprint(x, y, coldef, coldef, "［")
	coldef = termbox.ColorDefault | termbox.AttrReverse
	x += tbprint(x, y, coldef, coldef, "no filter")
	coldef = termbox.ColorDefault
	x += tbprint(x, y, coldef, coldef, "］")
	x += tbprint(x, y, coldef, coldef, "［+filter］")
	for ; x < w; x++ {
		termbox.SetCell(x, y, ' ', coldef, coldef)
	}

	// unicode box drawing chars around the edit box
	//	termbox.SetCell(midx-1, midy, '│', coldef, coldef)
	//	termbox.SetCell(midx+edit_box_width, midy, '│', coldef, coldef)
	//	termbox.SetCell(midx-1, midy-1, '┌', coldef, coldef)
	//	termbox.SetCell(midx-1, midy+1, '└', coldef, coldef)
	//	termbox.SetCell(midx+edit_box_width, midy-1, '┐', coldef, coldef)
	//	termbox.SetCell(midx+edit_box_width, midy+1, '┘', coldef, coldef)
	//	fill(midx, midy-1, edit_box_width, 1, termbox.Cell{Ch: '─'})
	//	fill(midx, midy+1, edit_box_width, 1, termbox.Cell{Ch: '─'})

	//	edit_box.Draw(midx, midy, edit_box_width, 1)
	//	termbox.SetCursor(midx+edit_box.CursorX(), midy)

	//	tbprint(midx+6, midy+3, coldef, coldef, "Press ESC to quit")
	termbox.Flush()
}

func main() {
	err := termbox.Init()
	if err != nil {
		panic(err)
	}
	defer termbox.Close()
	termbox.SetInputMode(termbox.InputEsc)

	d = &Device{}
	d.Open()

	render()
mainloop:
	for {
		switch ev := termbox.PollEvent(); ev.Type {
		case termbox.EventKey:
			switch ev.Key {
			case termbox.KeyEsc:
				break mainloop
				//			case termbox.KeyArrowLeft, termbox.KeyCtrlB:
				//				edit_box.MoveCursorOneRuneBackward()
				//			case termbox.KeyArrowRight, termbox.KeyCtrlF:
				//				edit_box.MoveCursorOneRuneForward()
				//			case termbox.KeyBackspace, termbox.KeyBackspace2:
				//				edit_box.DeleteRuneBackward()
				//			case termbox.KeyDelete, termbox.KeyCtrlD:
				//				edit_box.DeleteRuneForward()
				//			case termbox.KeyTab:
				//				edit_box.InsertRune('\t')
				//			case termbox.KeySpace:
				//				edit_box.InsertRune(' ')
				//			case termbox.KeyCtrlK:
				//				edit_box.DeleteTheRestOfTheLine()
				//			case termbox.KeyHome, termbox.KeyCtrlA:
				//				edit_box.MoveCursorToBeginningOfTheLine()
				//			case termbox.KeyEnd, termbox.KeyCtrlE:
				//				edit_box.MoveCursorToEndOfTheLine()
				//			default:
				//				if ev.Ch != 0 {
				//					edit_box.InsertRune(ev.Ch)
				//				}
			}
		case termbox.EventError:
			panic(ev.Err)
		}
		render()
	}
}
