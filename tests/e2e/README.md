# Bifrost E2E Tests

End-to-end tests for the Bifrost UI using Playwright.

## Setup

```bash
# Install dependencies
npm install

# Install Playwright browsers
npx playwright install
```

## Running Tests

```bash
# Run all tests
npm test

# Run tests in headed mode (visible browser)
npm run test:headed

# Run tests with Playwright UI
npm run test:ui

# Run tests in debug mode
npm run test:debug

# Run specific feature tests
npm run test:providers
npm run test:virtual-keys

# View test report
npm run report
```

## Folder Structure

```
tests/e2e/
├── playwright.config.ts    # Playwright configuration
├── core/                   # Shared utilities & fixtures
│   ├── fixtures/          # Custom test fixtures
│   ├── pages/             # Base page objects
│   ├── actions/           # Reusable actions
│   └── utils/             # Utilities and helpers
├── features/              # Feature-specific tests
│   ├── providers/         # Provider tests
│   └── virtual-keys/      # Virtual key tests
└── global-setup.ts        # Global test setup
```

## Writing Tests

### Using Page Objects

```typescript
import { test, expect } from '../../core/fixtures/base.fixture'

test('should create provider', async ({ providersPage }) => {
  await providersPage.goto()
  await providersPage.selectProvider('openai')
  // ...
})
```

### Test Data

Use factory functions from the `*.data.ts` files for generating test data:

```typescript
import { createProviderKeyData } from './providers.data'

const keyData = createProviderKeyData({ name: 'My Key' })
```

## Configuration

Environment variables:
- `BASE_URL` - Base URL of the application (default: http://localhost:3000)
- `CI` - Set to true in CI environments

## Debugging

```bash
# Run with Playwright Inspector
npm run test:debug

# Generate code with Codegen
npm run codegen
```
