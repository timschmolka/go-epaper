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
		return nil, err
	}

	port, err := spireg.Open("")
	if err != nil {
		return nil, err
	}

	conn, err := port.Connect(config.SPIFrequency, config.SPIMode, 8)
	if err != nil {
		closingErr := port.Close()
		if closingErr != nil {
			return nil, fmt.Errorf("could not close spi port %w", closingErr)
		}
		return nil, err
	}

	dc := gpioreg.ByName(config.DCPin)
	cs := gpioreg.ByName(config.CSPin)
	rst := gpioreg.ByName(config.RSTPin)
	busy := gpioreg.ByName(config.BUSYPin)

	if dc == nil || cs == nil || rst == nil || busy == nil {
		closingErr := port.Close()
		if closingErr != nil {
			return nil, fmt.Errorf("could not close spi port: %w", closingErr)
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
		err := d.Close()
		if err != nil {
			return nil, fmt.Errorf("could not close display handle: %w", err)
		}
		return nil, err
	}

	return d, nil
}

func (d *Display) Close() error {
	if err := d.Sleep(); err != nil {
		return err
	}
	return d.port.Close()
}

func (d *Display) Clear(white bool) error {
	var targetColor byte
	if white {
		targetColor = 0xFF
	} else {
		targetColor = 0x00
	}

	lineWidth := (d.width + 7) / 8
	buf := make([]byte, lineWidth*d.height)
	for i := range buf {
		buf[i] = targetColor
	}

	if err := d.sendCommand(0x24); err != nil {
		return err
	}
	if err := d.sendDataBulk(buf); err != nil {
		return err
	}

	return d.update()
}

func (d *Display) Sleep() error {
	if err := d.sendCommand(0x10); err != nil {
		return err
	}
	return d.sendData(0x01)
}

func (d *Display) DrawImage(img image.Image) error {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	lineWidth := (d.width + 7) / 8
	buf := make([]byte, lineWidth*d.height)

	palette := []color.Color{color.White, color.Black}
	palettedImg := image.NewPaletted(bounds, palette)
	draw.Draw(palettedImg, bounds, img, bounds.Min, draw.Src)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x >= d.width || y >= d.height {
				continue
			}

			colorIdx := palettedImg.ColorIndexAt(x, y)
			if colorIdx == 1 {
				byteIdx := x/8 + y*lineWidth
				bitIdx := uint(7 - x%8)
				buf[byteIdx] |= 1 << bitIdx
			}
		}
	}

	if err := d.sendCommand(0x24); err != nil {
		return err
	}
	if err := d.sendDataBulk(buf); err != nil {
		return err
	}

	return d.update()
}

func (d *Display) PartialDrawImage(img image.Image, xStart, yStart int) error {
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	if err := d.setWindow(xStart, yStart, xStart+width-1, yStart+height-1); err != nil {
		return fmt.Errorf("failed to set window: %w", err)
	}

	lineWidth := (d.width + 7) / 8
	buf := make([]byte, lineWidth*height)

	palette := []color.Color{color.White, color.Black}
	palettedImg := image.NewPaletted(bounds, palette)
	draw.Draw(palettedImg, bounds, img, bounds.Min, draw.Src)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x >= d.width || y >= d.height {
				continue
			}

			colorIdx := palettedImg.ColorIndexAt(x, y)
			if colorIdx == 1 {
				byteIdx := x/8 + y*lineWidth
				bitIdx := uint(7 - x%8)
				buf[byteIdx] |= 1 << bitIdx
			}
		}
	}

	if err := d.sendCommand(0x24); err != nil {
		return err
	}
	if err := d.sendDataBulk(buf); err != nil {
		return err
	}

	if err := d.partialUpdate(); err != nil {
		return fmt.Errorf("failed to perform partial update: %w", err)
	}

	return nil
}

func (d *Display) Size() (int, int) {
	return d.width, d.height
}

func (d *Display) init() error {
	if err := d.reset(); err != nil {
		return err
	}
	if err := d.waitBusy(); err != nil {
		return err
	}

	if err := d.sendCommand(0x12); err != nil {
		return err
	}
	d.ReadBusy()

	if err := d.sendCommand(0x01); err != nil {
		return err
	}
	heightMinusOne := d.height - 1
	if err := d.sendData(byte(heightMinusOne & 0xFF)); err != nil {
		return err
	}
	if err := d.sendData(byte((heightMinusOne >> 8) & 0xFF)); err != nil {
		return err
	}
	if err := d.sendData(0x00); err != nil {
		return err
	}

	if err := d.sendCommand(0x11); err != nil {
		return err
	}
	if err := d.sendData(0x03); err != nil {
		return err
	}

	if err := d.sendCommand(0x3C); err != nil {
		return err
	}
	if err := d.sendData(0x05); err != nil {
		return err
	}

	if err := d.sendCommand(0x2C); err != nil {
		return err
	}
	if err := d.sendData(0x80); err != nil {
		return err
	}

	return d.setWindow(0, 0, d.width-1, d.height-1)
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

func (d *Display) sendDataBulk(data []byte) error {
	if err := d.setPin(d.dc, gpio.High); err != nil {
		return err
	}
	if err := d.setPin(d.cs, gpio.Low); err != nil {
		return err
	}
	if err := d.conn.Tx(data, nil); err != nil {
		return err
	}
	return d.setPin(d.cs, gpio.High)
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

func (d *Display) ReadBusy() {
	for d.busy.Read() == gpio.High {
		time.Sleep(d.config.BusyPollTime)
	}
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

func (d *Display) update() error {
	if err := d.sendCommand(0x22); err != nil {
		return err
	}
	if err := d.sendData(0xF7); err != nil {
		return err
	}
	if err := d.sendCommand(0x20); err != nil {
		return err
	}
	return d.waitBusy()
}

func (d *Display) partialUpdate() error {
	if err := d.sendCommand(0x22); err != nil {
		return err
	}
	if err := d.sendData(0xC4); err != nil {
		return err
	}
	if err := d.sendCommand(0x20); err != nil {
		return err
	}
	return d.waitBusy()
}

func (d *Display) setWindow(xStart, yStart, xEnd, yEnd int) error {
	if err := d.sendCommand(0x44); err != nil {
		return err
	}
	if err := d.sendData(byte((xStart >> 3) & 0xFF)); err != nil {
		return err
	}
	if err := d.sendData(byte((xEnd >> 3) & 0xFF)); err != nil {
		return err
	}

	if err := d.sendCommand(0x45); err != nil {
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
	if err := d.sendData(byte((yEnd >> 8) & 0xFF)); err != nil {
		return err
	}
	return nil
}

func (d *Display) setPin(pin gpio.PinOut, level gpio.Level) error {
	if err := pin.Out(level); err != nil {
		return fmt.Errorf("failed to set pin: %w", err)
	}
	return nil
}
