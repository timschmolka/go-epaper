package epd

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
	"time"
)

const (
	cmdSoftwareReset         byte = 0x12
	cmdDriverOutputControl   byte = 0x01
	cmdDataEntryMode         byte = 0x11
	cmdSetRamXStartEndPos    byte = 0x44
	cmdSetRamYStartEndPos    byte = 0x45
	cmdSetRamXCounter        byte = 0x4E
	cmdSetRamYCounter        byte = 0x4F
	cmdBorderWaveformControl byte = 0x3C
	cmdDisplayUpdateControl1 byte = 0x21
	cmdDisplayUpdateControl2 byte = 0x22
	cmdWriteRAM              byte = 0x24
	cmdEnterDeepSleep        byte = 0x10

	dataEntryX                      byte = 0x03
	displayUpdateSequence           byte = 0x20
	displayUpdateSequenceNormalMode byte = 0xF7
)

type DisplayConfig struct {
	DCPin   string
	CSPin   string
	RSTPin  string
	BUSYPin string

	SPIFrequency physic.Frequency
	SPIMode      spi.Mode

	ResetHoldTime  time.Duration
	ResetDelayTime time.Duration
	BusyPollTime   time.Duration
	RefreshTimeout time.Duration

	OnBusyStateChange func(busy bool)
}

func DefaultConfig() DisplayConfig {
	return DisplayConfig{
		DCPin:   "GPIO25",
		CSPin:   "GPIO8",
		RSTPin:  "GPIO17",
		BUSYPin: "GPIO24",

		SPIFrequency: 1 * physic.MegaHertz,
		SPIMode:      spi.Mode0,

		ResetHoldTime:  20 * time.Millisecond,
		ResetDelayTime: 2 * time.Millisecond,
		BusyPollTime:   10 * time.Millisecond,
		RefreshTimeout: 10 * time.Second,

		OnBusyStateChange: nil,
	}
}

type Display struct {
	port   spi.PortCloser
	conn   spi.Conn
	dc     gpio.PinOut
	cs     gpio.PinOut
	rst    gpio.PinOut
	busy   gpio.PinIn
	width  int
	height int
	config DisplayConfig
}

func New() (*Display, error) {
	return NewWithConfig(DefaultConfig())
}

func NewWithConfig(config DisplayConfig) (*Display, error) {
	if _, err := host.Init(); err != nil {
		return nil, fmt.Errorf("host init failed: %w", err)
	}

	port, err := spireg.Open("")
	if err != nil {
		return nil, fmt.Errorf("SPI open failed: %w", err)
	}

	conn, err := port.Connect(config.SPIFrequency, config.SPIMode, 8)
	if err != nil {
		if closeErr := port.Close(); closeErr != nil {
			return nil, fmt.Errorf("SPI connect failed and port close failed: %w", closeErr)
		}
		return nil, fmt.Errorf("SPI connect failed: %w", err)
	}

	dc := gpioreg.ByName(config.DCPin)
	cs := gpioreg.ByName(config.CSPin)
	rst := gpioreg.ByName(config.RSTPin)
	busy := gpioreg.ByName(config.BUSYPin)

	if dc == nil || cs == nil || rst == nil || busy == nil {
		if closeErr := port.Close(); closeErr != nil {
			return nil, fmt.Errorf("GPIO init failed and port close failed: %w", closeErr)
		}
		return nil, errors.New("failed to initialize GPIO pins")
	}

	d := &Display{
		port:   port,
		conn:   conn,
		dc:     dc,
		cs:     cs,
		rst:    rst,
		busy:   busy,
		width:  122,
		height: 250,
		config: config,
	}

	if err := d.init(); err != nil {
		if closeErr := d.Close(); closeErr != nil {
			return nil, fmt.Errorf("display init failed and close failed: %w", closeErr)
		}
		return nil, fmt.Errorf("display init failed: %w", err)
	}

	return d, nil
}

func (d *Display) reset() error {
	if err := d.setPin(d.rst, gpio.High); err != nil {
		return err
	}
	time.Sleep(d.config.ResetHoldTime)

	if err := d.setPin(d.rst, gpio.Low); err != nil {
		return err
	}
	time.Sleep(d.config.ResetDelayTime)

	if err := d.setPin(d.rst, gpio.High); err != nil {
		return err
	}
	time.Sleep(d.config.ResetHoldTime)
	return nil
}

func (d *Display) waitBusy() error {
	if d.config.OnBusyStateChange != nil {
		d.config.OnBusyStateChange(true)
		defer d.config.OnBusyStateChange(false)
	}

	deadline := time.Now().Add(d.config.RefreshTimeout)
	for time.Now().Before(deadline) {
		if d.busy.Read() == gpio.Low {
			return nil
		}
		time.Sleep(d.config.BusyPollTime)
	}
	return errors.New("timeout waiting for display to be ready")
}

func (d *Display) sendDataBulk(data []byte) error {
	if err := d.setPin(d.dc, gpio.High); err != nil {
		return fmt.Errorf("DC pin set failed: %w", err)
	}
	if err := d.setPin(d.cs, gpio.Low); err != nil {
		return fmt.Errorf("CS pin set failed: %w", err)
	}
	if err := d.conn.Tx(data, nil); err != nil {
		return fmt.Errorf("bulk data transmission failed: %w", err)
	}
	return d.setPin(d.cs, gpio.High)
}

func (d *Display) setPin(pin gpio.PinOut, level gpio.Level) error {
	if err := pin.Out(level); err != nil {
		return fmt.Errorf("failed to set pin: %w", err)
	}
	return nil
}

func (d *Display) init() error {
	if err := d.reset(); err != nil {
		return err
	}
	if err := d.waitBusy(); err != nil {
		return err
	}

	if err := d.sendCommand(cmdSoftwareReset); err != nil {
		return err
	}
	if err := d.waitBusy(); err != nil {
		return err
	}

	if err := d.setDriverOutputControl(); err != nil {
		return err
	}

	if err := d.setDataEntryMode(dataEntryX); err != nil {
		return err
	}

	if err := d.setWindow(0, 0, d.width-1, d.height-1); err != nil {
		return err
	}

	if err := d.setBorderWaveform(); err != nil {
		return err
	}

	if err := d.sendCommand(cmdDisplayUpdateControl1); err != nil {
		return err
	}
	if err := d.sendData(0x00); err != nil {
		return err
	}
	if err := d.sendData(0x80); err != nil {
		return err
	}

	return d.waitBusy()
}

func (d *Display) setDriverOutputControl() error {
	if err := d.sendCommand(cmdDriverOutputControl); err != nil {
		return err
	}
	if err := d.sendData(0xf9); err != nil {
		return err
	}
	if err := d.sendData(0x00); err != nil {
		return err
	}
	return d.sendData(0x00)
}

func (d *Display) setDataEntryMode(mode byte) error {
	if err := d.sendCommand(cmdDataEntryMode); err != nil {
		return err
	}
	return d.sendData(mode)
}

func (d *Display) setBorderWaveform() error {
	if err := d.sendCommand(cmdBorderWaveformControl); err != nil {
		return err
	}
	return d.sendData(0x05)
}

func (d *Display) setWindow(xStart, yStart, xEnd, yEnd int) error {
	if err := d.sendCommand(cmdSetRamXStartEndPos); err != nil {
		return err
	}
	if err := d.sendData(byte((xStart >> 3) & 0xFF)); err != nil {
		return err
	}
	if err := d.sendData(byte((xEnd >> 3) & 0xFF)); err != nil {
		return err
	}

	if err := d.sendCommand(cmdSetRamYStartEndPos); err != nil {
		return err
	}
	if err := d.sendData(byte(yStart & 0xFF)); err != nil {
		return err
	}
	if err := d.sendData(byte((yStart >> 8) & 0xFF)); err != nil {
		return err
	}
	if err := d.sendData(byte(yEnd & 0xFF)); err != nil {
		return err
	}
	return d.sendData(byte((yEnd >> 8) & 0xFF))
}

func (d *Display) DrawImage(img image.Image) error {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	var sourceImg image.Image
	if width == d.height && height == d.width {
		rotated := image.NewRGBA(image.Rect(0, 0, height, width))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				rotated.Set(y, width-x-1, img.At(x, y))
			}
		}
		sourceImg = rotated
	} else if width == d.width && height == d.height {
		sourceImg = img
	} else {
		return fmt.Errorf("invalid image dimensions: must be %dx%d or %dx%d",
			d.width, d.height, d.height, d.width)
	}

	palette := []color.Color{color.Black, color.White}
	palettedImg := image.NewPaletted(sourceImg.Bounds(), palette)
	draw.Draw(palettedImg, palettedImg.Bounds(), sourceImg, image.Point{}, draw.Src)

	displayBuf, err := d.convertToDisplayBuffer(palettedImg)
	if err != nil {
		return err
	}

	if err := d.sendCommand(cmdWriteRAM); err != nil {
		return err
	}
	if err := d.sendDataBulk(displayBuf); err != nil {
		return err
	}

	return d.update()
}

func (d *Display) convertToDisplayBuffer(img *image.Paletted) ([]byte, error) {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	lineWidth := (d.width + 7) / 8
	buf := make([]byte, lineWidth*d.height)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x >= d.width || y >= d.height {
				continue
			}

			colorIdx := img.ColorIndexAt(x, y)
			if colorIdx == 1 {
				byteIdx := x/8 + y*lineWidth
				bitIdx := uint(7 - x%8)
				buf[byteIdx] |= 1 << bitIdx
			}
		}
	}

	return buf, nil
}

func (d *Display) update() error {
	if err := d.sendCommand(cmdDisplayUpdateControl2); err != nil {
		return err
	}
	if err := d.sendData(displayUpdateSequenceNormalMode); err != nil {
		return err
	}
	if err := d.sendCommand(displayUpdateSequence); err != nil {
		return err
	}
	return d.waitBusy()
}

func (d *Display) Clear(white bool) error {
	var targetColor byte
	if white {
		targetColor = 0xFF
	}

	lineWidth := (d.width + 7) / 8
	buf := make([]byte, lineWidth*d.height)
	for i := range buf {
		buf[i] = targetColor
	}

	if err := d.sendCommand(cmdWriteRAM); err != nil {
		return err
	}
	if err := d.sendDataBulk(buf); err != nil {
		return err
	}

	return d.update()
}

func (d *Display) Sleep() error {
	if err := d.sendCommand(cmdEnterDeepSleep); err != nil {
		return err
	}
	return d.sendData(0x01)
}

func (d *Display) Size() (int, int) {
	return d.width, d.height
}

func (d *Display) Close() error {
	if err := d.Sleep(); err != nil {
		return err
	}
	return d.port.Close()
}

func (d *Display) sendCommand(cmd byte) error {
	if err := d.setPin(d.dc, gpio.Low); err != nil {
		return err
	}
	if err := d.setPin(d.cs, gpio.Low); err != nil {
		return err
	}
	if err := d.conn.Tx([]byte{cmd}, nil); err != nil {
		return err
	}
	return d.setPin(d.cs, gpio.High)
}

func (d *Display) sendData(data byte) error {
	if err := d.setPin(d.dc, gpio.High); err != nil {
		return err
	}
	if err := d.setPin(d.cs, gpio.Low); err != nil {
		return err
	}
	if err := d.conn.Tx([]byte{data}, nil); err != nil {
		return err
	}
	return d.setPin(d.cs, gpio.High)
}
