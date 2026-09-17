// SPDX-License-Identifier: MIT

import { test, expect } from './coverage-fixtures.js'

const conversations = [
  { id: 'conversation-a', name: 'Alpha', messages: [], createdAt: 1, updatedAt: 1 },
  { id: 'conversation-b', name: 'Beta', messages: [], createdAt: 2, updatedAt: 2 },
]

async function installEventSource(page) {
  await page.addInitScript(() => {
    class TestEventSource extends EventTarget {
      static instances = []

      constructor(url) {
        super()
        this.url = url
        TestEventSource.instances.push(this)
        queueMicrotask(() => this.dispatchEvent(new Event('open')))
      }

      close() {}
    }

    window.EventSource = TestEventSource
    window.emitAgentEvent = (type, data) => {
      const event = new MessageEvent(type, { data: typeof data === 'string' ? data : JSON.stringify(data) })
      TestEventSource.instances.at(-1)?.dispatchEvent(event)
    }
    window.reopenAgentEvents = () => TestEventSource.instances.at(-1)?.dispatchEvent(new Event('open'))
  })
}

async function seedConversations(page, activeId = 'conversation-a') {
  await page.addInitScript(({ conversations, activeId }) => {
    localStorage.setItem('localai_agent_chats_demo', JSON.stringify({ conversations, activeId }))
  }, { conversations, activeId })
}

async function mockPending(page, value = { questions: [], plan: null }) {
  await page.route('**/api/agents/demo/pending**', route => route.fulfill({ json: value }))
}

test.describe('Agent chat interactions', () => {
  test.beforeEach(async ({ page }) => {
    await installEventSource(page)
    await seedConversations(page)
  })

  test('recovers a question, submits one option, and disables the resolved card', async ({ page }) => {
    let answer
    await mockPending(page, {
      questions: [{
        id: 'question-1', conversation_id: 'conversation-a', message_id: 'message-1', agent_id: 'planner',
        question: 'Which database?', options: ['SQLite', 'Postgres'], allow_free_text: true, timestamp: 1,
      }],
      plan: null,
    })
    await page.route('**/api/agents/demo/answer**', async route => {
      answer = route.request().postDataJSON()
      await route.fulfill({ json: { status: 'accepted' } })
    })

    await page.goto('/app/agents/demo/chat?user_id=test-user')
    const card = page.getByRole('group', { name: 'Question: Which database?' })
    await card.getByRole('button', { name: 'Postgres' }).click()

    expect(answer).toEqual({ question_id: 'question-1', selected: ['Postgres'], text: '' })
    await expect(card.getByRole('button', { name: 'Answered' })).toBeDisabled()
    await expect(card.getByRole('button', { name: 'Postgres' })).toBeDisabled()
  })

  test('submits a free-text answer from the composer while waiting for the user', async ({ page }) => {
    let answer
    await mockPending(page, {
      questions: [{
        id: 'question-2', conversation_id: 'conversation-a', message_id: 'message-2', agent_id: 'planner',
        question: 'What should change?', options: [], allow_free_text: true, timestamp: 2,
      }],
      plan: null,
    })
    await page.route('**/api/agents/demo/answer**', async route => {
      answer = route.request().postDataJSON()
      await route.fulfill({ json: { status: 'accepted' } })
    })

    await page.goto('/app/agents/demo/chat')
    await page.getByPlaceholder('Type a message...').fill('Use the existing schema')
    await page.getByRole('button', { name: 'Send message' }).click()

    expect(answer).toEqual({ question_id: 'question-2', selected: [], text: 'Use the existing schema' })
    await expect(page.getByRole('button', { name: 'Answered' })).toBeDisabled()
  })

  test('keeps a question actionable and displays answer request errors', async ({ page }) => {
    await mockPending(page, {
      questions: [{
        id: 'question-error', conversation_id: 'conversation-a', message_id: 'message-error', agent_id: 'planner',
        question: 'Choose a region', options: ['Local'], allow_free_text: false, timestamp: 2,
      }],
      plan: null,
    })
    await page.route('**/api/agents/demo/answer**', route => route.fulfill({
      status: 409,
      contentType: 'application/json',
      body: JSON.stringify({ error: 'This question has expired' }),
    }))

    await page.goto('/app/agents/demo/chat')
    const card = page.getByRole('group', { name: 'Question: Choose a region' })
    await card.getByRole('button', { name: 'Local' }).click()

    await expect(card.getByRole('alert')).toHaveText('This question has expired')
    await expect(card.getByRole('button', { name: 'Local' })).toBeEnabled()
  })

  test('does not overwrite a newer waiting-user event when an answer response is delayed', async ({ page }) => {
    let releaseAnswer
    let pending = {
      questions: [{
        id: 'question-old', conversation_id: 'conversation-a', message_id: 'message-old', agent_id: 'planner',
        question: 'First question', options: ['Continue'], allow_free_text: false, timestamp: 2,
      }],
      plan: null,
    }
    await page.route('**/api/agents/demo/pending**', route => route.fulfill({ json: pending }))
    await page.route('**/api/agents/demo/answer**', async route => {
      await new Promise(resolve => { releaseAnswer = resolve })
      await route.fulfill({ json: { status: 'accepted' } })
    })

    await page.goto('/app/agents/demo/chat')
    await page.getByRole('button', { name: 'Continue' }).click({ noWaitAfter: true })
    await expect.poll(() => typeof releaseAnswer).toBe('function')
    pending = {
      questions: [{
        id: 'question-new', conversation_id: 'conversation-a', message_id: 'message-new', agent_id: 'planner',
        question: 'Follow-up question', options: [], allow_free_text: true, timestamp: 3,
      }],
      plan: null,
    }
    await page.evaluate(() => window.emitAgentEvent('question', {
      id: 'question-new', conversation_id: 'conversation-a', message_id: 'message-new', agent_id: 'planner',
      question: 'Follow-up question', options: [], allow_free_text: true, timestamp: 3,
    }))
    releaseAnswer()

    await expect(page.getByRole('group', { name: 'Question: Follow-up question' })).toBeVisible()
    await expect(page.getByPlaceholder('Type a message...')).toBeEnabled()
  })

  test('preserves a sibling plan omitted from the oldest-only reconnect snapshot', async ({ page }) => {
    const plan = {
      id: 'oldest-plan', conversation_id: 'conversation-a', message_id: 'root-message',
      description: 'Oldest child', subtasks: ['Work'],
    }
    let pendingCalls = 0
    await page.route('**/api/agents/demo/pending**', route => {
      pendingCalls += 1
      return route.fulfill({ json: { questions: [], plan } })
    })
    await page.goto('/app/agents/demo/chat')
    await expect(page.getByRole('group', { name: 'Plan: Oldest child' })).toBeVisible()
    await page.evaluate(plan => window.emitAgentEvent('plan', {
      ...plan, id: 'sibling-plan', description: 'Sibling child',
    }), plan)
    const sibling = page.getByRole('group', { name: 'Plan: Sibling child' })
    await expect(sibling.getByRole('button', { name: 'Approve plan' })).toBeEnabled()
    const previousCalls = pendingCalls
    await page.evaluate(() => window.reopenAgentEvents())
    await expect.poll(() => pendingCalls).toBeGreaterThan(previousCalls)
    await expect(sibling.getByRole('button', { name: 'Approve plan' })).toBeEnabled()
  })

  test('recovers the next child plan after deciding the oldest pending plan', async ({ page }) => {
    const plans = ['First child', 'Second child'].map((description, index) => ({
      id: `child-plan-${index}`, conversation_id: 'conversation-a', message_id: 'root-message',
      agent_id: `child-${index}`, description, subtasks: ['Work'],
    }))
    await page.route('**/api/agents/demo/pending**', route => route.fulfill({ json: { questions: [], plan: plans[0] || null } }))
    await page.route('**/api/agents/demo/plan**', async route => {
      plans.shift()
      await route.fulfill({ json: { status: 'accepted' } })
    })
    await page.goto('/app/agents/demo/chat')
    await page.getByRole('group', { name: 'Plan: First child' }).getByRole('button', { name: 'Reject plan' }).click()
    await expect(page.getByRole('group', { name: 'Plan: Second child' }).getByRole('button', { name: 'Approve plan' })).toBeEnabled()
  })

  test('rejects an edited plan with empty feedback despite entered feedback', async ({ page }) => {
    let decision
    await mockPending(page, {
      questions: [],
      plan: {
        id: 'plan-1', conversation_id: 'conversation-a', message_id: 'message-3', description: 'Release safely',
        subtasks: ['Write tests', 'Deploy'], timestamp: 3,
      },
    })
    await page.route('**/api/agents/demo/plan**', async route => {
      decision = route.request().postDataJSON()
      await route.fulfill({ json: { status: 'accepted' } })
    })

    await page.goto('/app/agents/demo/chat')
    const plan = page.getByRole('group', { name: 'Plan: Release safely' })
    await plan.getByRole('textbox', { name: 'Subtask 1', exact: true }).fill('Run focused tests')
    await plan.getByRole('button', { name: 'Move subtask 2 up' }).click()
    await plan.getByRole('button', { name: 'Add subtask' }).click()
    await plan.getByRole('textbox', { name: 'Subtask 3', exact: true }).fill('Monitor rollout')
    await plan.getByLabel('Decision feedback').fill('Add a rollback step')
    await plan.getByRole('button', { name: 'Reject plan' }).click()

    expect(decision).toEqual({
      plan_id: 'plan-1', approved: false,
      subtasks: ['Deploy', 'Run focused tests', 'Monitor rollout'], feedback: '',
    })
    await expect(plan.getByRole('button', { name: 'Rejected' })).toBeDisabled()
  })

  test('approves a recovered plan after removing a subtask', async ({ page }) => {
    let decision
    await mockPending(page, {
      questions: [],
      plan: {
        id: 'plan-approve', conversation_id: 'conversation-a', message_id: 'message-approve', description: 'Focused plan',
        subtasks: ['Keep this', 'Remove this'], timestamp: 3,
      },
    })
    await page.route('**/api/agents/demo/plan**', async route => {
      decision = route.request().postDataJSON()
      await route.fulfill({ json: { status: 'accepted' } })
    })

    await page.goto('/app/agents/demo/chat')
    const plan = page.getByRole('group', { name: 'Plan: Focused plan' })
    await plan.getByRole('button', { name: 'Remove subtask 2' }).click()
    await plan.getByRole('button', { name: 'Approve plan' }).click()

    expect(decision).toEqual({ plan_id: 'plan-approve', approved: true, subtasks: ['Keep this'], feedback: '' })
    await expect(plan.getByRole('button', { name: 'Approved' })).toBeDisabled()
  })

  test('sends the selected conversation id and keeps waiting-agent conversations usable', async ({ page }) => {
    let chat
    await mockPending(page)
    await page.route('**/api/agents/demo/chat**', async route => {
      chat = route.request().postDataJSON()
      await route.fulfill({ json: { message_id: 'message-4' } })
    })
    await page.goto('/app/agents/demo/chat')

    await page.evaluate(() => window.emitAgentEvent('json_message_status', {
      status: 'waiting_agents', conversation_id: 'conversation-b', message_id: 'message-4',
    }))
    const beta = page.locator('.chat-list-item').filter({ hasText: 'Beta' })
    await page.waitForTimeout(100)
    await beta.evaluate(node => node.click())
    await expect(beta).toHaveClass(/active/)
    await page.getByPlaceholder('Type a message...').fill('Continue in beta')
    await page.getByRole('button', { name: 'Send message' }).click()
    expect(chat).toEqual({ message: 'Continue in beta', conversation_id: 'conversation-b' })

  })

  test('recovers a 409 pending question and preserves the rejected chat draft', async ({ page }) => {
    let questionAvailable = false
    await page.route('**/api/agents/demo/pending**', route => route.fulfill({ json: questionAvailable ? {
      questions: [{
        id: 'question-409', conversation_id: 'conversation-a', message_id: 'message-409', agent_id: 'planner',
        question: 'Clarify the audience', options: [], allow_free_text: true, timestamp: 4,
      }],
      plan: null,
    } : { questions: [], plan: null } }))
    await page.route('**/api/agents/demo/chat**', route => {
      questionAvailable = true
      return route.fulfill({
        status: 409,
        contentType: 'application/json',
        body: JSON.stringify({ pending_question_id: 'question-409' }),
      })
    })

    await page.goto('/app/agents/demo/chat')
    const composer = page.getByPlaceholder('Type a message...')
    await composer.fill('For maintainers')
    await page.getByRole('button', { name: 'Send message' }).click()

    await expect(page.getByRole('group', { name: 'Question: Clarify the audience' })).toBeVisible()
    await expect(composer).toHaveValue('For maintainers')
    await expect(page.locator('.chat-message-user', { hasText: 'For maintainers' })).toHaveCount(0)
  })

  test('reconciles on every SSE open without duplicating cards and shows sub-agent details', async ({ page }) => {
    let pendingCalls = 0
    await page.route('**/api/agents/demo/pending**', route => {
      pendingCalls += 1
      return route.fulfill({ json: {
        questions: [{
          id: 'question-3', conversation_id: 'conversation-a', message_id: 'message-5', agent_id: 'planner',
          question: 'Proceed?', options: ['Yes'], allow_free_text: false, timestamp: 4,
        }],
        plan: null,
      } })
    })
    await page.goto('/app/agents/demo/chat')
    await expect(page.getByText('Proceed?', { exact: true })).toHaveCount(1)
    await page.evaluate(() => window.reopenAgentEvents())
    await expect.poll(() => pendingCalls).toBeGreaterThanOrEqual(2)
    await expect(page.getByText('Proceed?', { exact: true })).toHaveCount(1)

    await page.evaluate(() => window.emitAgentEvent('sub_agent', {
      agent_id: 'researcher-1', agent_type: 'researcher', status: 'spawned', background: true,
      task: 'Check release notes', result_summary: '', timestamp: Date.now() - 2100,
      conversation_id: 'conversation-a', message_id: 'message-5',
    }))
    await page.evaluate(() => window.emitAgentEvent('sub_agent', {
      agent_id: 'researcher-1', agent_type: 'researcher', status: 'completed', background: true,
      task: 'Check release notes', result_summary: 'No breaking changes found', timestamp: Date.now(),
      conversation_id: 'conversation-a', message_id: 'message-5',
    }))
    const activity = page.getByRole('button', { name: /researcher.*researcher-1.*completed/i })
    await expect(activity).toBeVisible()
    await expect(activity).toContainText('2s')
    await activity.click()
    await expect(page.getByText('No breaking changes found')).toBeVisible()
  })

  test('keeps an earlier question before a later agent reply in the transcript', async ({ page }) => {
    await mockPending(page, {
      questions: [{
        id: 'question-order', conversation_id: 'conversation-a', message_id: 'message-order', agent_id: 'planner',
        question: 'Earlier question', options: ['Answer'], allow_free_text: false, timestamp: Date.now(),
      }],
      plan: null,
    })
    await page.goto('/app/agents/demo/chat')
    await expect(page.getByText('Earlier question', { exact: true })).toHaveCount(1)
    await page.evaluate(() => window.emitAgentEvent('json_message', {
      id: 'later-reply', sender: 'agent', content: 'Later reply', conversation_id: 'conversation-a',
      message_id: 'later-reply-agent', timestamp: '1970-01-01T00:00:01Z', metadata: {},
    }))

    const ordered = await page.evaluate(() => {
      const question = [...document.querySelectorAll('.agent-interaction-title')].find(node => node.textContent === 'Earlier question')
      const reply = [...document.querySelectorAll('.chat-message-content')].find(node => node.textContent.includes('Later reply'))
      return Boolean(question && reply && (question.compareDocumentPosition(reply) & Node.DOCUMENT_POSITION_FOLLOWING))
    })
    expect(ordered).toBe(true)
  })

  test('does not expire a question emitted while an older pending snapshot is in flight', async ({ page }) => {
    let releaseFirst
    let calls = 0
    await page.route('**/api/agents/demo/pending**', async route => {
      calls += 1
      if (calls === 1) {
        await new Promise(resolve => { releaseFirst = resolve })
        await route.fulfill({ json: { questions: [], plan: null } })
        return
      }
      await route.fulfill({ json: {
        questions: [{
          id: 'question-race', conversation_id: 'conversation-a', message_id: 'message-race', agent_id: 'planner',
          question: 'Keep this question?', options: ['Keep'], allow_free_text: false, timestamp: 5,
        }],
        plan: null,
      } })
    })

    await page.goto('/app/agents/demo/chat')
    await expect.poll(() => typeof releaseFirst).toBe('function')
    await page.evaluate(() => window.emitAgentEvent('question', {
      id: 'question-race', conversation_id: 'conversation-a', message_id: 'message-race', agent_id: 'planner',
      question: 'Keep this question?', options: ['Keep'], allow_free_text: false, timestamp: 5,
    }))
    releaseFirst()

    const card = page.getByRole('group', { name: 'Question: Keep this question?' })
    await expect(card.getByRole('button', { name: 'Keep' })).toBeEnabled()
    await expect.poll(() => calls).toBeGreaterThanOrEqual(2)
  })

  test('does not restore a stale processing lock after reload', async ({ page }) => {
    await page.addInitScript(conversations => {
      localStorage.setItem('localai_agent_chats_demo', JSON.stringify({
        conversations: conversations.map((conversation, index) => index === 0 ? {
          ...conversation,
          status: 'processing',
          stream: { content: 'stale partial response', reasoning: '', toolCalls: [] },
        } : conversation),
        activeId: 'conversation-a',
      }))
    }, conversations)
    await mockPending(page)

    await page.goto('/app/agents/demo/chat')

    await expect(page.getByPlaceholder('Type a message...')).toBeEnabled()
    await expect(page.getByText('stale partial response')).toHaveCount(0)
  })
})
