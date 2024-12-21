package main

import (
	"log"
	"time"

	"go-epaper/epd"
)

func main() {
	config := epd.DefaultConfig()
	config.OnBusyStateChange = func(busy bool) {
		if busy {
			log.Println("Display is refreshing...")
		} else {
			log.Println("Display refresh complete")
		}
	}

	display, err := epd.NewWithConfig(config)
	if err != nil {
		log.Fatal(err)
	}
	defer display.Close()

	if err := display.Clear(true); err != nil {
		log.Fatal(err)
	}

	time.Sleep(time.Second)

	if err := display.Clear(false); err != nil {
		log.Fatal(err)
	}

	time.Sleep(time.Second)

	if err := display.Clear(true); err != nil {
		log.Fatal(err)
	}
}
