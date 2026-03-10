package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"victron_smartevse/global"
	"victron_smartevse/smartevse"
	"victron_smartevse/victron"

	"github.com/quokka2020/gohelpers/mqtthelper"
	"github.com/quokka2020/gohelpers/util"

	// needed to add timezone into the binary
	_ "time/tzdata"
)

var mqtt_prefix = util.GetEnv("MQTT_PREFIX", "victron_smartevse")
var log_file = util.GetEnv("LOG_FILE", "")

func main() {
	if log_file != "" {
		logFile, err := os.OpenFile(log_file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("Failed to open log file: %v", err)
		}
		defer logFile.Close()

		log.SetOutput(logFile)
	}
	interrupt := make(chan os.Signal, 1)
	sigquit := make(chan os.Signal, 1)

	signal.Notify(interrupt, os.Interrupt)
	signal.Notify(sigquit, syscall.SIGQUIT)

	flag.Parse()
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	log.Printf("Starting version %s BuildTime: %s", global.Version, global.BuildTime)

	mqtt := mqtthelper.CreateMqttHelper(mqtt_prefix)
	defer mqtt.Close()

	ev, err := smartevse.NewEvHandler(mqtt)
	if err != nil {
		log.Fatalf("Failed to init smartevse err:%v", err)
	}
	defer ev.Close()

	victron, err := victron.NewVictronHandler()
	if err != nil {
		log.Fatalf("Failed to init victron err:%v", err)
	}
	defer victron.Close()

	err = ev.RegisterInVictron(victron)
	if err != nil {
		log.Fatalf("Failed to init victron err:%v", err)
	}

	go victron.Listen()

	// Give
	<-time.After(5 * time.Second)

	log.Printf("Starting loop")
loop:
	for {
		// victron.ListNames()
		// current.PublishAll()
		ev.Write_MainsMeter()
		ev.Write_HomeBattery()

		select {
		case <-time.After(2 * time.Second):
			// case <-time.After(10 * time.Second):

		case <-sigquit:
			log.Printf("Received a sigquit")
			break loop
		case <-interrupt:
			log.Printf("Received an interrupt")
			break loop
		}
	}
	log.Print("Done")
}
