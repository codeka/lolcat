package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/nsf/termbox-go"
)

// BufferLineCount is the number of lines of buffer to keep in memory from logcat.
const BufferLineCount = 1000

// devices is the list of devices that we currently know about.
var devices []*Device

// deviceIndex the index into devices that we're currently displaying.
var deviceIndex int

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
}

// Device is all the stuff we know about a single attached device.
type Device struct {
	// ID is the identfiied of the device, that you'd pass to adb's "-s" parameter
	ID string

	// Name is the display name of the device, that we show in the UI.
	Name string

	logBuffer *LogBuffer

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
		index := lb.nextLineIndex - int(lb.lineNo-lineNo) - 1
		if index < 0 {
			index += len(lb.lines)
		}
		if index < 0 {
			break
		}
		res[i] = lb.lines[index]
		i++
	}
	return res
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
		lastLineNo := logBuffer.GetLastLineNo()
		firstLineNo := lastLineNo - int64(h) - 3
		lines := logBuffer.GetLines(firstLineNo, lastLineNo)
		devices[deviceIndex].mutex.Unlock()

		coldef = termbox.ColorDefault
		for i := 0; i < len(lines); i++ {
			y := h - 2 - i
			tbprint(0, y, coldef, coldef, lines[i])
		}
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

	termbox.Flush()
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
			if ev.Key == termbox.KeyEsc {
				break mainloop
			}
			render()

		case <-devices[deviceIndex].ping:
			render()
		}
	}
}
