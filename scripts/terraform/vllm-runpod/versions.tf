terraform {
  required_version = ">= 1.9"

  required_providers {
    # runpod/runpod (official) fails to load its own schema as of 1.0.9, so use the community provider.
    runpod = {
      source  = "decentralized-infrastructure/runpod"
      version = "= 1.0.1"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

# Reads RUNPOD_API_KEY from the environment.
provider "runpod" {}
