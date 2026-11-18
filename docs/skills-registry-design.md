# Skill Registration System Design Document

> **Design specification** — not a deployable config file. This document describes the architecture for a config-driven skill registration system in Bifrost (Go). A future PR will ship the Go implementation.

## Overview

This document proposes a configuration-driven skill registration system that decouples skill definitions from hardcoded code. Skills are defined via declarative configuration files (YAML) and dynamically loaded at runtime, with support for hot-reload and conditional execution.

Traditional skill registration relies on decorators or manual imports, tightly coupling the skill list to core code. Adding or modifying a skill requires editing core code and redeploying. This design uses external configuration files for skill metadata, enabling automatic registration at startup, hot-reload, and conditional loading.

**Note on language**: This is a Go project (Bifrost AI Gateway). The examples below use Python-style pseudocode for clarity of the design concepts. A Go implementation will follow in a subsequent PR.

---

## Detailed Design

### Core Concepts

- **Skill Definition**: Each skill has a unique ID, name, description, module path, dependencies, and execution conditions.
- **Configuration-Driven**: Skill registration data is stored in `skills.yaml` or `skills.json`. The system loads it via a config parser.
- **Dynamic Loading**: Using Go's plugin system or a registry pattern, skill implementations are loaded dynamically from config-defined paths.
- **Registry**: An in-memory skill registry (`SkillRegistry`) provides registration, querying, enable/disable, and unload interfaces.

### Configuration Structure

```yaml
# skills.yaml — example configuration
version: "1.0"
skills:
  - id: "weather_query"
    name: "Weather Query"
    description: "Query current weather by city name"
    module: "skills.weather.handler"
    function: "GetWeather"
    enabled: true
    priority: 10
    dependencies:
      - "net/http"
    conditions:
      - type: "time_range"
        params:
          start: "06:00"
          end: "23:00"
    metadata:
      author: "team-ai"
      version: "1.0.1"

  - id: "calculator"
    name: "Calculator"
    description: "Perform basic arithmetic operations"
    module: "skills.math.calc"
    function: "Calculate"
    enabled: true
    priority: 5
    dependencies: []
    conditions: []
    metadata:
      author: "core"
      version: "1.0.0"
```

### Registry Implementation (Design Pseudocode)

The following pseudocode illustrates the design. The final Go implementation will use Go idioms (interfaces, goroutines, etc.).

```python
# skill_registry.py — DESIGN PSEUDOCODE (for illustration only)
# The Go implementation will differ in syntax but follow the same architecture.

import yaml
import importlib
import logging
import threading
from typing import Dict, Optional, List
from dataclasses import dataclass, field

logger = logging.getLogger(__name__)

# Whitelist of allowed module paths — security boundary
ALLOWED_MODULES = {
    "skills.weather.handler",
    "skills.math.calc",
    "skills.entertainment.jokes",
    # Add new modules here after security review
}


def _validate_module_path(module_path: str) -> bool:
    """Whitelist check: only pre-approved modules can be dynamically loaded."""
    if module_path not in ALLOWED_MODULES:
        logger.error(f"Module not in whitelist: {module_path}")
        return False
    # Reject dangerous patterns even within whitelisted paths
    forbidden = ["__import__", "os.", "subprocess", "sys.", "eval", "exec"]
    for pattern in forbidden:
        if pattern in module_path.lower():
            logger.error(f"Dangerous module path rejected: {module_path}")
            return False
    return True


@dataclass
class SkillDefinition:
    id: str
    name: str
    description: str
    module: str
    function: str
    enabled: bool = True
    priority: int = 0
    dependencies: List[str] = field(default_factory=list)
    conditions: List[Dict] = field(default_factory=list)
    metadata: Dict = field(default_factory=dict)


class SkillRegistry:
    """Thread-safe skill registry with atomic hot-reload support."""

    def __init__(self, config_path: str = "skills.yaml"):
        self.config_path = config_path
        self._lock = threading.RLock()
        self._skills: Dict[str, SkillDefinition] = {}
        self._loaded_modules: Dict[str, object] = {}

    def load_config(self) -> None:
        """Load and parse the configuration file (thread-safe)."""
        with self._lock:
            self._load_config_internal()

    def _load_config_internal(self) -> None:
        """Internal load — caller must hold _lock."""
        try:
            with open(self.config_path, 'r') as f:
                data = yaml.safe_load(f)
        except FileNotFoundError:
            logger.error(f"Config file not found: {self.config_path}")
            raise
        except yaml.YAMLError as e:
            logger.error(f"Config file parse error: {e}")
            raise

        if data.get("version") != "1.0":
            logger.warning("Config version mismatch — trying compat mode")

        for skill_data in data.get("skills", []):
            try:
                skill = SkillDefinition(**skill_data)
                self._skills[skill.id] = skill
                logger.info(f"Registered skill: {skill.id} ({skill.name})")
            except Exception as e:
                logger.error(f"Failed to parse skill: {skill_data.get('id', 'unknown')} - {e}")

    def register_skill(self, skill: SkillDefinition) -> None:
        """Register a single skill manually (thread-safe)."""
        with self._lock:
            if skill.id in self._skills:
                logger.warning(f"Skill {skill.id} already exists — will be overwritten")
            self._skills[skill.id] = skill

    def get_skill(self, skill_id: str) -> Optional[SkillDefinition]:
        """Get skill definition by ID (thread-safe)."""
        with self._lock:
            return self._skills.get(skill_id)

    def load_skill_module(self, skill_id: str) -> Optional[object]:
        """Dynamically load a skill module (thread-safe, with whitelist)."""
        with self._lock:
            skill = self._skills.get(skill_id)
            if not skill:
                logger.error(f"Skill {skill_id} not registered")
                return None

            if skill_id in self._loaded_modules:
                return self._loaded_modules[skill_id]

            # Security: whitelist check before import
            if not _validate_module_path(skill.module):
                return None

            try:
                module = importlib.import_module(skill.module)
                if not hasattr(module, skill.function):
                    raise AttributeError(
                        f"Function {skill.function} not found in module {skill.module}"
                    )
                self._loaded_modules[skill_id] = module
                logger.debug(f"Loaded skill module: {skill.module}")
                return module
            except ImportError as e:
                logger.error(f"Failed to import module {skill.module}: {e}")
                return None
            except AttributeError as e:
                logger.error(f"Skill function not found: {e}")
                return None

    def execute_skill(self, skill_id: str, *args, **kwargs) -> Optional[object]:
        """Execute a skill by ID (thread-safe)."""
        with self._lock:
            skill = self._skills.get(skill_id)
            if not skill or not skill.enabled:
                logger.warning(f"Skill {skill_id} not enabled or not found")
                return None

            if not self._check_conditions(skill):
                logger.info(f"Skill {skill_id} conditions not met — skipping")
                return None

            module = self.load_skill_module(skill_id)
            if not module:
                return None

            try:
                func = getattr(module, skill.function)
                return func(*args, **kwargs)
            except Exception as e:
                logger.error(f"Failed to execute skill {skill_id}: {e}")
                return None

    def _check_conditions(self, skill: SkillDefinition) -> bool:
        """Check skill execution conditions (caller must hold _lock)."""
        from datetime import datetime, time

        for condition in skill.conditions:
            if condition["type"] == "time_range":
                now = datetime.now().time()
                start = time.fromisoformat(condition["params"]["start"])
                end = time.fromisoformat(condition["params"]["end"])
                if not (start <= now <= end):
                    return False
            # Extensible: add more condition types here
        return True

    def list_skills(self, enabled_only: bool = True) -> List[SkillDefinition]:
        """List all registered skills (thread-safe)."""
        with self._lock:
            if enabled_only:
                return [s for s in self._skills.values() if s.enabled]
            return list(self._skills.values())

    def atomic_reload(self) -> bool:
        """Atomically reload config — old state is preserved on failure.

        This prevents the race condition where a failed reload leaves
        the registry empty.
        """
        # 1. Load new config into temporary storage
        new_skills: Dict[str, SkillDefinition] = {}
        try:
            with open(self.config_path, 'r') as f:
                data = yaml.safe_load(f)
            for skill_data in data.get("skills", []):
                skill = SkillDefinition(**skill_data)
                new_skills[skill.id] = skill
        except Exception as e:
            logger.error(f"Atomic reload failed — keeping existing state: {e}")
            return False

        # 2. Swap atomically under lock
        with self._lock:
            self._skills.clear()
            self._skills.update(new_skills)
            self._loaded_modules.clear()  # Modules will be re-imported on demand

        logger.info(f"Atomic reload complete: {len(new_skills)} skills loaded")
        return True
```

### Skill Implementation Example

The corresponding weather query skill handler:

```python
# skills/weather/handler.py — DESIGN EXAMPLE
# Note: In production, the API key MUST be supplied via secure configuration,
# NOT hardcoded or embedded in source code.

import requests
from typing import Optional

# Use a config-managed credential; never hardcode secrets
API_KEY = None  # Populated from secure config at startup


def get_weather(city: str, api_key: Optional[str] = None) -> Optional[str]:
    """
    Query weather by city name.

    Args:
        city: City name (e.g., "Beijing", "Tokyo")
        api_key: API key from secure config; must be provided at runtime

    Returns:
        Weather description string, or None on failure
    """
    effective_key = api_key or API_KEY
    if not effective_key:
        logger.error("No API key configured for weather skill")
        return None

    base_url = "https://api.openweathermap.org/data/2.5/weather"
    params = {
        "q": city,
        "appid": effective_key,
        "units": "metric",
        "lang": "en",
    }

    try:
        response = requests.get(base_url, params=params, timeout=5)
        response.raise_for_status()
        data = response.json()

        temp = data["main"]["temp"]
        desc = data["weather"][0]["description"]
        humidity = data["main"]["humidity"]

        return f"{city}: {desc}, {temp}°C, humidity {humidity}%"
    except requests.RequestException as e:
        logger.error(f"Weather query failed: {e}")
        return None
```

**Security note on credentials**: Credentials must never appear in source code or documentation examples. Use environment variables, a secrets manager, or Bifrost's config store instead. The `"default_key"` fallback pattern is explicitly forbidden — it silently sends credentials to external services if misconfigured.

---

## Usage Examples

### Basic Workflow

```python
# main.py — USAGE EXAMPLE
from skill_registry import SkillRegistry

# Initialize registry
registry = SkillRegistry("skills.yaml")
registry.load_config()

# List available skills
skills = registry.list_skills()
print(f"Registered {len(skills)} skills:")
for s in skills:
    print(f"  - {s.id}: {s.name}")

# Execute a skill
result = registry.execute_skill("weather_query", city="Shanghai")
print(result)

# Dynamically register a skill at runtime
from skill_registry import SkillDefinition
new_skill = SkillDefinition(
    id="joke_teller",
    name="Joke Teller",
    description="Tell a random joke",
    module="skills.entertainment.jokes",
    function="tell_joke",
    enabled=True,
    priority=1,
)
registry.register_skill(new_skill)
```

### Hot-Reload with Atomic Swap

```python
# hot_reload.py — USAGE EXAMPLE
import time
from watchdog.observers import Observer
from watchdog.events import FileSystemEventHandler


class ConfigWatcher(FileSystemEventHandler):
    def __init__(self, registry):
        self.registry = registry

    def on_modified(self, event):
        if event.src_path.endswith("skills.yaml"):
            logger.info("Config change detected — performing atomic reload...")
            success = self.registry.atomic_reload()
            if success:
                logger.info("Hot-reload completed successfully")
            else:
                logger.warning("Hot-reload failed — previous state preserved")


# Start file watcher
observer = Observer()
observer.schedule(ConfigWatcher(registry), path=".", recursive=False)
observer.start()
```

**Why atomic reload?** The naive approach of `clear()` then `load_config()` creates a window where the registry is empty — any concurrent `execute_skill()` call will fail with "skill not registered". The `atomic_reload()` method loads the new config into temporary storage first, then swaps atomically under a lock.

---

## Important Considerations

### 1. Security

- **Module whitelist**: Dynamic imports are restricted to a pre-approved whitelist (`ALLOWED_MODULES`). Any module not in the whitelist is rejected before import.
- **Path validation**: Reject dangerous module paths containing `__import__`, `os.`, `subprocess`, `sys.`, `eval`, `exec`.
- **Config integrity**: In production, use digital signatures or checksums to verify config file integrity.
- **Credentials**: Never embed secrets in config files or code examples. Use Bifrost's config store or environment variables.

### 2. Dependency Management

The `dependencies` field in the config is metadata only — it does not trigger automatic installation. In a Go context, dependencies should be managed via `go.mod`. For Python-based skills (if using an embedded runtime), consider integrating with `pip` or `poetry` with validation at registration time.

### 3. Performance

- **Lazy loading**: Skill modules are imported on first execution only. For frequently called skills, consider pre-loading at startup.
- **Module caching**: Loaded modules are cached in `_loaded_modules`. Clearing this cache (on hot-reload) triggers re-imports on next use.
- **Stateless design**: Skill implementations should be stateless functions to avoid global state side effects.

### 4. Error Isolation

- A single skill's load or execution failure must not affect other skills.
- Use `try-except` (or Go's `recover`) to capture errors and log detailed information.
- Consider isolating each skill in a separate execution context or sandbox.

### 5. Configuration Validation

Use JSON Schema (see `transports/config.schema.json` in the Bifrost repo) or Pydantic-style validation to ensure field types and required fields are correct:

```python
from pydantic import BaseModel, Field, validator


class SkillConfig(BaseModel):
    id: str = Field(..., regex=r'^[a-z_][a-z0-9_]*$')
    name: str
    description: str = ""
    module: str
    function: str
    enabled: bool = True
    priority: int = Field(ge=0, le=100, default=0)
    dependencies: List[str] = []
    conditions: List[Dict] = []
    metadata: Dict = {}

    @validator('module')
    def validate_module_path(cls, v):
        if '.' not in v:
            raise ValueError('Module path must contain at least one dot')
        return v
```

### 6. Extensibility

The current design supports `time_range` conditions. Additional condition types (`user_role`, `rate_limit`, `feature_flag`) can be added by implementing the strategy pattern for condition checks.

### 7. Testing

- Mock the filesystem and module imports to avoid external dependencies.
- Cover: config parsing, skill registration, condition checking, error paths, concurrent access (race condition tests), and hot-reload atomicity.
- For the Go implementation, use `testing/quick` or property-based testing to verify thread safety.

---

> **Status**: Design specification. This document defines the architecture; the Go implementation will ship in a follow-up PR that adds a `SkillRegistry` to the Bifrost core with Go concurrency primitives (sync.RWMutex, atomic.Swap patterns, plugin loading via `plugin.Open`).
