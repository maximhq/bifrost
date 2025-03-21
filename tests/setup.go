package tests

import (
	"bifrost"
	"bifrost/interfaces"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/maximhq/maxim-go"
	"github.com/maximhq/maxim-go/logging"
)

func loadEnv() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file:", err)
	}
}

func getPlugin() (interfaces.Plugin, error) {
	loadEnv()

	mx := maxim.Init(&maxim.MaximSDKConfig{ApiKey: os.Getenv("MAXIM_API_KEY")})

	logger, err := mx.GetLogger(&logging.LoggerConfig{Id: os.Getenv("MAXIM_LOGGER_ID")})
	if err != nil {
		return nil, err
	}

	plugin := &Plugin{logger}

	return plugin, nil
}

func getBifrost() (*bifrost.Bifrost, error) {
	loadEnv()

	account := BaseAccount{}
	plugin, err := getPlugin()
	if err != nil {
		fmt.Println("Error setting up the plugin:", err)
		return nil, err
	}

	bifrost, err := bifrost.Init(&account, []interfaces.Plugin{plugin})
	if err != nil {
		return nil, err
	}

	return bifrost, nil
}
