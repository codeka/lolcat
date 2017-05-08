package main

import (
	"bufio"
	"container/ring"
	"fmt"
	"os"
	"os/exec"
	"strings"
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

// Device is all the stuff we know about a single attached device.
type Device struct {
	// ID is the identfiied of the device, that you'd pass to adb's "-s" parameter
	ID string

	// Name is the display name of the device, that we show in the UI.
	Name string

	logcat *ring.Ring

	waiting bool
	ping    chan int
}

func (d *Device) appendLine(line string) {
	d.logcat = d.logcat.Prev()
	d.logcat.Value = line

	if d.waiting {
		d.ping <- 1
	}
}

// Open opens a connection to the given device via an adb command. Basically we start streaming
// logcat output to the device's AbdContext.
func (d *Device) Open() {
	d.logcat = ring.New(BufferLineCount)
	d.ping = make(chan int)
	d.waiting = false

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
				if thisTime.UnixNano()-lastTime.UnixNano() > 1000000000 {
					// More than a second passed, we can start notifying listeners of new updates
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
		logcat := devices[deviceIndex].logcat
		coldef = termbox.ColorDefault
		for y := h - 3; y >= 1; y-- {
			if logcat.Value == nil {
				break
			}
			tbprint(0, y, coldef, coldef, logcat.Value.(string))
			logcat = logcat.Next()
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
			fmt.Fprintf(os.Stderr, "Not a device line: '%s'", line)
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

		d := &Device{
			ID:   id,
			Name: strings.Replace(name, "_", " ", -1),
		}
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
