/**
 * Centralized selectors for Bifrost UI elements
 * Using data-testid attributes where available, falling back to other strategies
 */

export const Selectors = {
  // Common
  toast: '[data-sonner-toast]:not([data-removed="true"])',
  loadingSpinner: '[data-testid="loading-spinner"]',

  // Providers Page
  providers: {
    // Sidebar list
    providerList: '[data-testid="provider-list"]',
    providerItem: (name: string) => `[data-testid="provider-item-${name.replace(/[^a-z0-9]+/gi, "-").toLowerCase()}"]`,
    addProviderBtn: '[data-testid="add-provider-btn"]',
    
    // Provider config
    providerConfig: '[data-testid="provider-config"]',
    addKeyBtn: '[data-testid="add-key-btn"]',
    keysTable: '[data-testid="keys-table"]',
    keyRow: (name: string) => `[data-testid="key-row-${name}"]`,
    
    // Key form
    keyForm: {
      container: '[data-testid="key-form"]',
      nameInput: '[data-testid="key-name-input"]',
      valueInput: '[data-testid="key-value-input"]',
      modelsInput: '[data-testid="key-models-input"]',
      weightInput: '[data-testid="key-weight-input"]',
      saveBtn: '[data-testid="key-save-btn"]',
      cancelBtn: '[data-testid="key-cancel-btn"]',
    },
    
    // Custom provider sheet
    customProviderSheet: {
      container: '[data-testid="custom-provider-sheet"]',
      nameInput: '[data-testid="custom-provider-name"]',
      baseProviderSelect: '[data-testid="base-provider-select"]',
      baseUrlInput: '[data-testid="base-url-input"]',
      saveBtn: '[data-testid="custom-provider-save-btn"]',
      cancelBtn: '[data-testid="custom-provider-cancel-btn"]',
    },
  },

  // Virtual Keys Page
  virtualKeys: {
    // Table
    table: '[data-testid="vk-table"]',
    row: (name: string) => `[data-testid="vk-row-${name}"]`,
    createBtn: '[data-testid="create-vk-btn"]',
    
    // Sheet/Form
    sheet: {
      container: '[data-testid="vk-sheet"]',
      nameInput: '[data-testid="vk-name-input"]',
      descriptionInput: '[data-testid="vk-description-input"]',
      isActiveToggle: '[data-testid="vk-is-active-toggle"]',
      
      // Provider configs
      providerSelect: '[data-testid="vk-provider-select"]',

      // Entity assignment
      entityTypeSelect: '[data-testid="vk-entity-type-select"]',
      teamSelect: '[data-testid="vk-team-select"]',
      customerSelect: '[data-testid="vk-customer-select"]',
      
      // Actions
      saveBtn: '[data-testid="vk-save-btn"]',
      cancelBtn: '[data-testid="vk-cancel-btn"]',
    },
  },

  // User Groups Page
  userGroups: {
    teamsTab: '[data-testid="teams-tab"]',
    customersTab: '[data-testid="customers-tab"]',
    teamsTable: '[data-testid="teams-table"]',
    customersTable: '[data-testid="customers-table"]',
    createTeamBtn: '[data-testid="create-team-btn"]',
    createCustomerBtn: '[data-testid="customer-button-create"]',
  },

  // Common form elements
  form: {
    input: (name: string) => `[data-testid="input-${name}"]`,
    select: (name: string) => `[data-testid="select-${name}"]`,
    toggle: (name: string) => `[data-testid="toggle-${name}"]`,
    saveBtn: '[data-testid="btn-save"]',
    cancelBtn: '[data-testid="btn-cancel"]',
    deleteBtn: '[data-testid="btn-delete"]',
  },

  // Dialogs
  dialog: {
    container: '[role="dialog"]',
    confirmBtn: '[data-testid="dialog-confirm-btn"]',
    cancelBtn: '[data-testid="dialog-cancel-btn"]',
  },
}
