// SPDX-License-Identifier: MIT

import { test, expect } from './coverage-fixtures.js'

const fields = [
  { name: 'name', label: 'Name', type: 'text', defaultValue: '', required: true, tags: { section: 'BasicInfo' } },
  { name: 'model', label: 'Model', type: 'text', defaultValue: '', required: true, tags: { section: 'ModelSettings' } },
  { name: 'enable_sub_agents', label: 'Enable Sub-Agents', type: 'checkbox', defaultValue: false, tags: { section: 'AdvancedSettings' } },
  { name: 'sub_agents', label: 'Local Sub-Agents', type: 'textarea', defaultValue: [], tags: { section: 'AdvancedSettings' } },
  { name: 'remote_agents', label: 'Remote Agents', type: 'textarea', defaultValue: [], tags: { section: 'AdvancedSettings' } },
]

test('round-trips delegation arrays and masks remote API key fields', async ({ page }) => {
  await page.route('**/api/agents/config/metadata', route => route.fulfill({
    json: { Fields: fields, Actions: [], Connectors: [], Filters: [] },
  }))
  await page.route('**/api/agents/delegator/config', route => route.fulfill({
    json: {
      name: 'delegator',
      model: 'test-model',
      enable_sub_agents: true,
      sub_agents: ['researcher'],
      remote_agents: [{
        name: 'reviewer',
        description: 'Checks the result',
        url: 'https://agents.example.test',
        api_key: 'secret-token',
      }],
    },
  }))
  await page.route('**/api/agents/skills', route => route.fulfill({ json: { skills: [] } }))

  let saved
  await page.route('**/api/agents/delegator', async route => {
    if (route.request().method() === 'PUT') {
      saved = route.request().postDataJSON()
      await route.fulfill({ json: { status: 'updated' } })
      return
    }
    await route.continue()
  })

  await page.goto('/app/agents/delegator/edit')
  await page.getByText('Advanced', { exact: true }).click()

  const delegation = page.getByTestId('agent-delegation-fields')
  await expect(delegation.getByRole('textbox', { name: 'Local agent 1', exact: true })).toHaveValue('researcher')
  await expect(delegation.getByLabel('Remote agent 1 API key')).toHaveAttribute('type', 'password')
  await expect(delegation.getByLabel('Remote agent 1 API key')).toHaveValue('secret-token')

  await delegation.getByRole('button', { name: 'Add local agent' }).click()
  await delegation.getByRole('textbox', { name: 'Local agent 2', exact: true }).fill('writer')
  await delegation.getByRole('button', { name: 'Add remote agent' }).click()
  await delegation.getByLabel('Remote agent 2 name').fill('formatter')
  await delegation.getByLabel('Remote agent 2 URL').fill('https://formatter.example.test')
  await delegation.getByLabel('Remote agent 2 API key').fill('another-secret')

  await page.getByRole('button', { name: 'Save Changes' }).click()
  await expect.poll(() => saved).toBeTruthy()
  expect(saved.sub_agents).toEqual(['researcher', 'writer'])
  expect(saved.remote_agents).toEqual([
    {
      name: 'reviewer',
      description: 'Checks the result',
      url: 'https://agents.example.test',
      api_key: 'secret-token',
    },
    {
      name: 'formatter',
      description: '',
      url: 'https://formatter.example.test',
      api_key: 'another-secret',
    },
  ])
})

test('submits meaningful empty delegation arrays for a new agent', async ({ page }) => {
  await page.route('**/api/agents/config/metadata', route => route.fulfill({
    json: { Fields: fields, Actions: [], Connectors: [], Filters: [] },
  }))
  await page.route('**/api/agents/skills', route => route.fulfill({ json: { skills: [] } }))

  let created
  await page.route('**/api/agents', async route => {
    if (route.request().method() === 'POST') {
      created = route.request().postDataJSON()
      await route.fulfill({ json: { status: 'created' } })
      return
    }
    await route.continue()
  })

  await page.goto('/app/agents/new')
  await page.locator('#field-name').fill('delegator')
  await page.getByText('Model Settings', { exact: true }).click()
  await page.getByRole('textbox', { name: 'Type or select a model...' }).fill('test-model')
  await page.getByText('Advanced', { exact: true }).click()
  const subAgentToggle = page.locator('.form-row', { hasText: 'Enable Sub-Agents' })
  await subAgentToggle.locator('label.toggle').click()
  await expect(subAgentToggle.getByRole('checkbox')).toBeChecked()
  await expect(page.getByTestId('agent-delegation-fields')).toContainText('An empty list allows every other local agent.')
  await page.getByRole('button', { name: 'Create Agent' }).click()

  await expect.poll(() => created).toBeTruthy()
  expect(created.sub_agents).toEqual([])
  expect(created.remote_agents).toEqual([])
})
