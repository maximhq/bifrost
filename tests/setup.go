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

	configs := map[interfaces.SupportedModelProvider]interfaces.ProviderConfig{
		interfaces.OpenAI: {
			NetworkConfig: interfaces.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
			},
		},
		interfaces.Anthropic: {
			NetworkConfig: interfaces.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
			},
		},
		interfaces.Bedrock: {
			NetworkConfig: interfaces.NetworkConfig{
				DefaultRequestTimeoutInSeconds: 30,
			},
			MetaConfig: &interfaces.MetaConfig{
				BedrockMetaConfig: &interfaces.BedrockMetaConfig{
					SecretAccessKey: "AMpq95pNadM2fD1GlcNvjbMiGhizwYaGKJxv+nti",
					Region:          maxim.StrPtr("us-east-1"),
				},
			},
		},
	}

	bifrost, err := bifrost.Init(&account, []interfaces.Plugin{plugin}, configs)
	if err != nil {
		return nil, err
	}

	return bifrost, nil
}
