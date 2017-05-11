package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
	"github.com/nsf/termbox-go"
)

// BufferLineCount is the number of lines of buffer to keep in memory from logcat.
const BufferLineCount = 1000

// PreferredHorizontalThreshold ??
const PreferredHorizontalThreshold = 5

// devices is the list of devices that we currently know about.
var devices []*Device

// deviceIndex the index into devices that we're currently displaying.
var deviceIndex int

// viewIndex the current LogView that we're looking at (0 == the full LogBuffer, 1 == the first one
// etc)
var viewIndex int

// The EditBox we're writing into
var editbox EditBox

// EditBox represents the box where you're currently typing text.
type EditBox struct {
	text              []byte
	visualOffset      int
	cursorOffsetBytes int
	cursorOffsetCells int
	cursorOffsetRunes int
}

// LogBuffer represents a fixed-size buffer of log lines. The lines are indexed with 0 being the
// oldest line and N being the most recent log line. Older logs will be expired, but the index
// will never be reused (this means a LogView can reference expired lines without having to
// update the views every time a line is expired).
type LogBuffer struct {
	lines []string

	// nextLineIndex is the index into the lines[] slice that the *next* log line will go.
	nextLineIndex int

	// lineNo is the line number (starting from 1) that the *most recent* log line has. This number
	// increments for every line that's added to the buffer, whereas nextLineIndex wraps around as
	// the buffer fills up.
	lineNo int64
}

// LogView is a "view" over a device's logs. There's a special view that represents all logs, and
// then there is zero or more LogView's for filtered results.
type LogView struct {
	Name string

	lb     *LogBuffer
	filter *regexp.Regexp
	index  []int64
}

// Device is all the stuff we know about a single attached device.
type Device struct {
	// ID is the identfiied of the device, that you'd pass to adb's "-s" parameter
	ID string

	// Name is the display name of the device, that we show in the UI.
	Name string

	logBuffer *LogBuffer
	logViews  []*LogView

	// mutex is used to synchronize access to the log buffer.
	mutex *sync.Mutex

	waiting bool
	ping    chan int
}

func (d *Device) appendLine(line string) {
	d.mutex.Lock()
	d.logBuffer.lines[d.logBuffer.nextLineIndex] = line
	d.logBuffer.lineNo++
	d.logBuffer.nextLineIndex++
	if d.logBuffer.nextLineIndex >= len(d.logBuffer.lines) {
		d.logBuffer.nextLineIndex = 0
	}
	d.mutex.Unlock()

	if d.waiting {
		d.ping <- 1
	}
}

// NewDevice creates a new instance of Device for the device with the given ID and name.
func NewDevice(id, name string) *Device {
	return &Device{
		ID:   id,
		Name: name,
		logBuffer: &LogBuffer{
			lines:         make([]string, BufferLineCount),
			nextLineIndex: 0,
			lineNo:        0,
		},
		mutex:   &sync.Mutex{},
		ping:    make(chan int),
		waiting: false,
	}
}

// Open opens a connection to the given device via an adb command. Basically we start streaming
// logcat output to the device's AbdContext.
func (d *Device) Open() {
	cmd := exec.Command("adb", "-s", d.ID, "logcat", "-v", "threadtime")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic("An error occurred reading output: " + err.Error())
	}
	scanner := bufio.NewScanner(stdout)
	go func() {
		lastTime := time.Now()
		for scanner.Scan() {
			if !d.waiting {
				thisTime := time.Now()
				if thisTime.UnixNano()-lastTime.UnixNano() > 500000000 {
					// More than 1/2 second passed, we can start notifying listeners of new updates
					d.waiting = true
				}
				lastTime = thisTime
			}
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

// LineNoToIndex converts the given line number to an index into the lines buffer.
func (lb *LogBuffer) LineNoToIndex(lineNo int64) int {
	index := lb.nextLineIndex - int(lb.lineNo-lineNo) - 1
	if index < 0 {
		index += len(lb.lines)
	}
	if index < 0 || index >= len(lb.lines) {
		return -1
	}
	return index
}

// GetLastLineNo returns the index of the last line in the log buffer.
// You should only call this method when you've got the device's mutex locked.
func (lb *LogBuffer) GetLastLineNo() int64 {
	return lb.lineNo
}

// GetLines returns a slice of the lines from the given line number to the given line number.
// You should only call this method when you've got the device's mutex locked.
func (lb *LogBuffer) GetLines(from, to int64) []string {
	if from < 0 {
		from = 0
	}
	if from == to {
		return make([]string, 0)
	}

	// TODO: can we keep these in a buffer to avoid allocating the new array each time?
	res := make([]string, int(to-from))
	i := 0
	for lineNo := to; lineNo > from; lineNo-- {
		index := lb.LineNoToIndex(lineNo)
		if index < 0 {
			break
		}
		res[i] = lb.lines[index]
		i++
	}
	return res
}

// UpdateFilter refreshes the filter for the current LogView to be the given regex.
func (lv *LogView) UpdateFilter(lb *LogBuffer, str string) {
	runes := []rune(str)
	if len(runes) == 0 {
		lv.Name = "<empty>"
	} else if len(runes) > 16 {
		runes = runes[:16]
		lv.Name = string(runes[:16]) + "..."
	} else {
		lv.Name = str
	}

	filter, err := regexp.Compile(str)
	if err != nil {
		lv.filter = nil
		lv.Name = "#ERR#"
	} else {
		lv.filter = filter
	}

	lv.index = nil
	for no := lb.lineNo - int64(len(lb.lines)); no <= lb.lineNo; no++ {
		if no <= 0 {
			continue
		}
		index := lb.LineNoToIndex(no)
		if lv.filter == nil || lv.filter.MatchString(lb.lines[index]) {
			lv.index = append(lv.index, no)
		}
	}
}

// GetLastLineNo returns the index of the last line in the log buffer.
// You should only call this method when you've got the device's mutex locked.
func (lv *LogView) GetLastLineNo() int64 {
	if lv.index == nil {
		return 0
	}

	return lv.index[len(lv.index)-1]
}

// GetLines returns a slice of the lines with the given line no at the end, and count elements big.
func (lv *LogView) GetLines(bottomLineNo int64, count int) []string {
	// TODO: can we keep these in a buffer to avoid allocating the new array each time?
	res := make([]string, int(count))
	ri := count - 1
	for i := len(lv.index) - 1; i >= 0; i-- {
		if lv.index[i] > bottomLineNo {
			continue
		}
		index := lv.lb.LineNoToIndex(lv.index[i])
		if index < 0 {
			break
		}
		res[ri] = lv.lb.lines[index]
		ri--
		if ri < 0 {
			break
		}
	}
	return res
}

// Draw draws the EditBox in the given location
func (eb *EditBox) Draw(x, y, w int) {
	eb.AdjustVisualOffset(w)

	const coldef = termbox.ColorDefault
	fill(x, y, w, 1, termbox.Cell{Ch: ' '})

	t := eb.text
	lx := 0
	tabstop := 0
	for {
		rx := lx - eb.visualOffset
		if len(t) == 0 {
			break
		}

		if rx >= w {
			termbox.SetCell(x+w-1, y, '→',
				coldef, coldef)
			break
		}

		r, size := utf8.DecodeRune(t)
		if r == '\t' {
			for ; lx < tabstop; lx++ {
				rx = lx - eb.visualOffset
				if rx >= w {
					goto next
				}

				if rx >= 0 {
					termbox.SetCell(x+rx, y, ' ', coldef, coldef)
				}
			}
		} else {
			if rx >= 0 {
				termbox.SetCell(x+rx, y, r, coldef, coldef)
			}
			lx += runewidth.RuneWidth(r)
		}
	next:
		t = t[size:]
	}

	if eb.visualOffset != 0 {
		termbox.SetCell(x, y, '←', coldef, coldef)
	}
}

// AdjustVisualOffset adjusts line visual offset to a proper value depending on width
func (eb *EditBox) AdjustVisualOffset(width int) {
	ht := PreferredHorizontalThreshold
	maxHorizontalThreshold := (width - 1) / 2
	if ht > maxHorizontalThreshold {
		ht = maxHorizontalThreshold
	}

	threshold := width - 1
	if eb.visualOffset != 0 {
		threshold = width - ht
	}
	if eb.cursorOffsetCells-eb.visualOffset >= threshold {
		eb.visualOffset = eb.cursorOffsetCells + (ht - width + 1)
	}

	if eb.visualOffset != 0 && eb.cursorOffsetCells-eb.visualOffset < ht {
		eb.visualOffset = eb.cursorOffsetCells - ht
		if eb.visualOffset < 0 {
			eb.visualOffset = 0
		}
	}
}

func adjustOffset(text []byte, offsetBytes int) (offsetCells, offsetRunes int) {
	text = text[:offsetBytes]
	for len(text) > 0 {
		r, size := utf8.DecodeRune(text)
		text = text[size:]
		offsetRunes++
		offsetCells += runewidth.RuneWidth(r)
	}
	return
}

// MoveCursorTo moves the cursor to the given byte offset.
func (eb *EditBox) MoveCursorTo(offsetBytes int) {
	eb.cursorOffsetBytes = offsetBytes
	eb.cursorOffsetCells, eb.cursorOffsetCells = adjustOffset(eb.text, offsetBytes)
}

// RuneUnderCursor returns the rune (and it's size) under the cursor.
func (eb *EditBox) RuneUnderCursor() (rune, int) {
	return utf8.DecodeRune(eb.text[eb.cursorOffsetBytes:])
}

// RuneBeforeCursor returns the rune (and it's size) before the cursor.
func (eb *EditBox) RuneBeforeCursor() (rune, int) {
	return utf8.DecodeLastRune(eb.text[:eb.cursorOffsetBytes])
}

// MoveCursorOneRuneBackward moves the cursor one rune to the left (if possible)
func (eb *EditBox) MoveCursorOneRuneBackward() {
	if eb.cursorOffsetBytes == 0 {
		return
	}
	_, size := eb.RuneBeforeCursor()
	eb.MoveCursorTo(eb.cursorOffsetBytes - size)
}

// MoveCursorOneRuneForward moves the cursor one run to the right (if possible)
func (eb *EditBox) MoveCursorOneRuneForward() {
	if eb.cursorOffsetBytes == len(eb.text) {
		return
	}
	_, size := eb.RuneUnderCursor()
	eb.MoveCursorTo(eb.cursorOffsetBytes + size)
}

// MoveCursorToBeginningOfTheLine moves the cursor to the beginning of the line.
func (eb *EditBox) MoveCursorToBeginningOfTheLine() {
	eb.MoveCursorTo(0)
}

// MoveCursorToEndOfTheLine moves the cursor to the end of the line.
func (eb *EditBox) MoveCursorToEndOfTheLine() {
	eb.MoveCursorTo(len(eb.text))
}

// DeleteRuneBackward delets the rune to the left of the cursor.
func (eb *EditBox) DeleteRuneBackward() {
	if eb.cursorOffsetBytes == 0 {
		return
	}

	eb.MoveCursorOneRuneBackward()
	_, size := eb.RuneUnderCursor()
	eb.text = byteSliceRemove(eb.text, eb.cursorOffsetBytes, eb.cursorOffsetBytes+size)
}

// DeleteRuneForward deletes the rune to the right of the cursor.
func (eb *EditBox) DeleteRuneForward() {
	if eb.cursorOffsetBytes == len(eb.text) {
		return
	}
	_, size := eb.RuneUnderCursor()
	eb.text = byteSliceRemove(eb.text, eb.cursorOffsetBytes, eb.cursorOffsetBytes+size)
}

// DeleteTheRestOfTheLine deletes everything to the right of the cursor.
func (eb *EditBox) DeleteTheRestOfTheLine() {
	eb.text = eb.text[:eb.cursorOffsetBytes]
}

// InsertRune inserts the given rune at the current cursor position.
func (eb *EditBox) InsertRune(r rune) {
	var buf [utf8.UTFMax]byte
	n := utf8.EncodeRune(buf[:], r)
	eb.text = byteSliceInsert(eb.text, eb.cursorOffsetBytes, buf[:n])
	eb.MoveCursorOneRuneForward()
}

// CursorX ...
// Please, keep in mind that cursor depends on the value of visualOffset, which
// is being set on Draw() call, so.. call this method after Draw() one.
func (eb *EditBox) CursorX() int {
	return eb.cursorOffsetCells - eb.visualOffset
}

func byteSliceRemove(text []byte, from, to int) []byte {
	size := to - from
	copy(text[from:], text[to:])
	text = text[:len(text)-size]
	return text
}

func byteSliceGrow(s []byte, desiredCap int) []byte {
	if cap(s) < desiredCap {
		ns := make([]byte, len(s), desiredCap)
		copy(ns, s)
		return ns
	}
	return s
}

func byteSliceInsert(text []byte, offset int, what []byte) []byte {
	n := len(text) + len(what)
	text = byteSliceGrow(text, n)
	text = text[:n]
	copy(text[offset+len(what):], text[offset:])
	copy(text[offset:], what)
	return text
}

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

func fill(x, y, w, h int, cell termbox.Cell) {
	for ly := 0; ly < h; ly++ {
		for lx := 0; lx < w; lx++ {
			termbox.SetCell(x+lx, y+ly, cell.Ch, cell.Fg, cell.Bg)
		}
	}
}

func render() {
	coldef := termbox.ColorDefault
	termbox.Clear(coldef, coldef)
	w, h := termbox.Size()

	// Top line, device list
	x := 0
	coldef = termbox.ColorDefault | termbox.AttrReverse
	for _, d := range devices {
		x += tbprint(x, 0, coldef, coldef, "［")
		coldef = termbox.ColorDefault
		x += tbprint(x, 0, coldef, coldef, d.Name)
		coldef = termbox.ColorDefault | termbox.AttrReverse
		x += tbprint(x, 0, coldef, coldef, "］")
	}
	for ; x < w; x++ {
		termbox.SetCell(x, 0, ' ', coldef, coldef)
	}

	// Start from bottom and write up
	if len(devices) > deviceIndex {
		logBuffer := devices[deviceIndex].logBuffer
		devices[deviceIndex].mutex.Lock()
		var lines []string
		if viewIndex == 0 {
			lastLineNo := logBuffer.GetLastLineNo()
			firstLineNo := lastLineNo - int64(h) + 3
			lines = logBuffer.GetLines(firstLineNo, lastLineNo)
		} else {
			lastLineNo := logBuffer.GetLastLineNo()
			count := h - 3
			lines = devices[deviceIndex].logViews[viewIndex-1].GetLines(lastLineNo, count)
		}
		devices[deviceIndex].mutex.Unlock()

		coldef = termbox.ColorDefault
		for i := 0; i < len(lines); i++ {
			y := h - 3 - i
			tbprint(0, y, coldef, coldef, lines[i])
		}
	}

	// Second from bottom line, filter.
	// TODO: the first tab ("no filter") should have no filter line
	y := h - 2
	editbox.Draw(1, y, w-2)
	termbox.SetCursor(1+editbox.cursorOffsetCells, y)

	// Last line, tabs, one tab per configured filter
	x = 0
	y = h - 1
	coldef = termbox.ColorDefault
	x += tbprint(x, y, coldef, coldef, " ")
	if viewIndex == 0 {
		coldef = termbox.ColorDefault | termbox.AttrReverse
	}
	x += tbprint(x, y, coldef, coldef, "no filter")
	coldef = termbox.ColorDefault
	x += tbprint(x, y, coldef, coldef, "  ")

	for n, view := range devices[deviceIndex].logViews {
		if viewIndex-1 == n {
			coldef = termbox.ColorDefault | termbox.AttrReverse
		}
		x += tbprint(x, y, coldef, coldef, view.Name)
		coldef = termbox.ColorDefault
		x += tbprint(x, y, coldef, coldef, "  ")
	}

	x += tbprint(x, y, coldef, coldef, "+filter")
	for ; x < w; x++ {
		termbox.SetCell(x, y, ' ', coldef, coldef)
	}

	termbox.Flush()
}

// moveViewRight moves the selected view one to the right. If there's no more views, we'll create
// a new one with an empty filter.
func moveViewRight() {
	device := devices[deviceIndex]
	viewIndex++
	if (viewIndex - 1) == len(device.logViews) {
		device.logViews = append(device.logViews, &LogView{
			Name: "<empty>",
			lb:   device.logBuffer,
		})
	}
	editbox.MoveCursorToBeginningOfTheLine()
	editbox.DeleteTheRestOfTheLine()
	render()
}

func updateCurrentView() {
	device := devices[deviceIndex]
	if viewIndex > 0 {
		device.mutex.Lock()
		device.logViews[viewIndex-1].UpdateFilter(device.logBuffer, string(editbox.text))
		device.mutex.Unlock()
	}
}

// refreshDevices refreshes the list of attached devices (by running 'adb devices' basically).
func refreshDevices() {
	cmd := exec.Command("adb", "devices", "-l")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic("'adb devices' error: " + err.Error())
	}

	scanner := bufio.NewScanner(stdout)
	err = cmd.Start()
	if err != nil {
		panic("'adb devices' error: " + err.Error())
	}

	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.Fields(line)
		if len(parts) < 2 || parts[1] != "device" {
			fmt.Fprintf(os.Stderr, "Not a device line: '%s'\n", line)
			continue
		}

		id := parts[0]
		name := id
		for i := 2; i < len(parts); i++ {
			kvp := strings.Split(parts[i], ":")
			if len(kvp) == 2 && kvp[0] == "model" {
				name = kvp[1]
			}
		}

		d := NewDevice(id, strings.Replace(name, "_", " ", -1))
		d.Open()
		devices = append(devices, d)

		deviceIndex = 0
		viewIndex = 0
	}
	if err := scanner.Err(); err != nil {
		panic("An error occurred reading output: " + err.Error())
	}
}

func main() {
	err := termbox.Init()
	if err != nil {
		panic(err)
	}
	defer termbox.Close()
	termbox.SetInputMode(termbox.InputEsc)

	refreshDevices()
	render()

	events := make(chan termbox.Event)
	go func() {
		for {
			events <- termbox.PollEvent()
		}
	}()

mainloop:
	for {
		select {
		case ev := <-events:
			switch ev.Key {
			case termbox.KeyEsc:
				break mainloop
			case termbox.KeyTab:
				// TODO: if shift pressed, move left
				moveViewRight()
			case termbox.KeyArrowLeft, termbox.KeyCtrlB:
				editbox.MoveCursorOneRuneBackward()
			case termbox.KeyArrowRight, termbox.KeyCtrlF:
				editbox.MoveCursorOneRuneForward()
			case termbox.KeyBackspace, termbox.KeyBackspace2:
				editbox.DeleteRuneBackward()
			case termbox.KeyDelete, termbox.KeyCtrlD:
				editbox.DeleteRuneForward()
			case termbox.KeySpace:
				editbox.InsertRune(' ')
			case termbox.KeyCtrlK:
				editbox.DeleteTheRestOfTheLine()
			case termbox.KeyHome, termbox.KeyCtrlA:
				editbox.MoveCursorToBeginningOfTheLine()
			case termbox.KeyEnd, termbox.KeyCtrlE:
				editbox.MoveCursorToEndOfTheLine()
			default:
				if ev.Ch != 0 {
					editbox.InsertRune(ev.Ch)
				}
			}
			updateCurrentView()
			render()
		case <-devices[deviceIndex].ping:
			render()
		}
	}
}
