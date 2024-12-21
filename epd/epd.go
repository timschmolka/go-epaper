package epd

import (
	"errors"
	"image"
	"time"
	
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

type DisplayConfig struct {
	DCPin   string
	CSPin   string
	RSTPin  string
	BUSYPin string
	
	SPIFrequency physic.Frequency
	SPIMode      spi.Mode

	ResetHoldTime   time.Duration
	ResetDelayTime  time.Duration
	BusyPollTime    time.Duration
	RefreshTimeout  time.Duration

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

		ResetHoldTime:   20 * time.Millisecond,
		ResetDelayTime:  2 * time.Millisecond,
		BusyPollTime:    10 * time.Millisecond,
		RefreshTimeout:  10 * time.Second,
		
		OnBusyStateChange: nil,
	}
}

type Display struct {
	port    spi.PortCloser
	conn    spi.Conn
	dc      gpio.PinOut
	cs      gpio.PinOut
	rst     gpio.PinOut
	busy    gpio.PinIn
	width   int
	height  int
	config  DisplayConfig
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
		port.Close()
		return nil, err
	}

	dc := gpioreg.ByName(config.DCPin)
	cs := gpioreg.ByName(config.CSPin)
	rst := gpioreg.ByName(config.RSTPin)
	busy := gpioreg.ByName(config.BUSYPin)

	if dc == nil || cs == nil || rst == nil || busy == nil {
		port.Close()
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
		d.Close()
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
	color := byte(0x00)
	if white {
		color = 0xFF
	}

	linewidth := (d.width + 7) / 8
	buf := make([]byte, linewidth*d.height)
	for i := range buf {
		buf[i] = color
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

	linewidth := (d.width + 7) / 8
	buf := make([]byte, linewidth*d.height)

	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			if x >= d.width || y >= d.height {
				continue
			}
			
			r, g, b, _ := img.At(x, y).RGBA()
			var pixel byte
			if (r+g+b)/3 > 0x7FFF {
				pixel = 1
			}

			byteIdx := x/8 + y*linewidth
			bitIdx := uint(7 - x%8)
			if pixel == 0 {
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

func (d *Display) Size() (int, int) {
	return d.width, d.height
}

func (d *Display) init() error {
	d.reset()
	d.waitBusy()

	if err := d.sendCommand(0x12); err != nil {
		return err
	}
	d.waitBusy()

	if err := d.sendCommand(0x01); err != nil {
		return err
	}
	if err := d.sendData(0xf9); err != nil {
		return err
	}
	if err := d.sendData(0x00); err != nil {
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

	return d.setWindow(0, 0, d.width-1, d.height-1)
}

func (d *Display) sendCommand(cmd byte) error {
	d.dc.Out(gpio.Low)
	d.cs.Out(gpio.Low)
	err := d.conn.Tx([]byte{cmd}, nil)
	d.cs.Out(gpio.High)
	return err
}

func (d *Display) sendData(data byte) error {
	d.dc.Out(gpio.High)
	d.cs.Out(gpio.Low)
	err := d.conn.Tx([]byte{data}, nil)
	d.cs.Out(gpio.High)
	return err
}

func (d *Display) sendDataBulk(data []byte) error {
	d.dc.Out(gpio.High)
	d.cs.Out(gpio.Low)
	err := d.conn.Tx(data, nil)
	d.cs.Out(gpio.High)
	return err
}

func (d *Display) reset() {
	d.rst.Out(gpio.High)
	time.Sleep(d.config.ResetHoldTime)
	d.rst.Out(gpio.Low)
	time.Sleep(d.config.ResetDelayTime)
	d.rst.Out(gpio.High)
	time.Sleep(d.config.ResetHoldTime)
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
	return d.sendData(byte((yEnd >> 8) & 0xFF))
}
