# go-epaper

A Go library for interfacing with the Waveshare 2.13" v4 E-Paper Display. Clean, configurable, and easy to use.

## Features

- Simple API for common operations
- Fully configurable GPIO pins and timings
- Zero external dependencies beyond periph.io
- Busy-state callbacks for integration with UI frameworks
- Hardware error handling with timeouts

## Installation

```bash
go get github.com/yourusername/go-epaper
```

## Quick Start

```go
display, err := epaper.New()
if err != nil {
    log.Fatal(err)
}
defer display.Close()

display.Clear(true)  // Clear to white
```

## Configuration

All aspects of the display can be configured:

```go
config := epaper.DefaultConfig()

// Custom GPIO pins
config.DCPin = "GPIO25"
config.CSPin = "GPIO8"
config.RSTPin = "GPIO17"
config.BUSYPin = "GPIO24"

// Custom timing
config.RefreshTimeout = 15 * time.Second
config.BusyPollTime = 5 * time.Millisecond

// Get notified of display refreshes
config.OnBusyStateChange = func(busy bool) {
    log.Printf("Display busy: %v", busy)
}

display, err := epaper.NewWithConfig(config)
```

## Hardware Setup

Standard Waveshare 2.13" v4 E-Paper connections:

```
Display  ->  Raspberry Pi
BUSY     ->  GPIO24
RST      ->  GPIO17
DC       ->  GPIO25
CS       ->  GPIO8
CLK      ->  SPI CLK
DIN      ->  SPI MOSI
GND      ->  Ground
VCC      ->  3.3V
```

## API Examples

Draw an image:
```go
img := createImage() // your image creation logic
if err := display.DrawImage(img); err != nil {
    log.Fatal(err)
}
```

Get display dimensions:
```go
width, height := display.Size()
```

Put display to sleep to save power:
```go
if err := display.Sleep(); err != nil {
    log.Fatal(err)
}
```

## Requirements

- Go 1.21 or newer
- Raspberry Pi or similar board with SPI support
- periph.io for GPIO and SPI communication

## License

MIT License - see LICENSE file

## Contributing

Contributions welcome! Please feel free to submit a Pull Request.
