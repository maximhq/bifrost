package tests

import (
	"bifrost"
	"log"

	"github.com/joho/godotenv"
)

func loadEnv() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file:", err)
	}
}

func getBifrost() (*bifrost.Bifrost, error) {
	loadEnv()

	account := BaseAccount{}

	bifrost, err := bifrost.Init(&account)
	if err != nil {
		return nil, err
	}

	return bifrost, nil
}
