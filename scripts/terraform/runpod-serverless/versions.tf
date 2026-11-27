terraform {
  required_version = ">= 1.9"

  required_providers {
    runpod = {
      source  = "runpod/runpod"
      version = "= 2.0.0-rc.3"
    }
  }
}

# Reads RUNPOD_API_KEY from the environment.
provider "runpod" {}