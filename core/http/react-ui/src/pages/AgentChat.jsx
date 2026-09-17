import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import { useParams, useOutletContext, useSearchParams } from 'react-router-dom'
import { agentsApi } from '../utils/api'
import { apiUrl } from '../utils/basePath'
import { renderMarkdown, highlightAll, enhanceCodeBlocks } from '../utils/markdown'
import { extractCodeArtifacts, extractMetadataArtifacts, renderMarkdownWithArtifacts } from '../utils/artifacts'
import CanvasPanel from '../components/CanvasPanel'
import ResourceCards from '../components/ResourceCards'
import ConfirmDialog from '../components/ConfirmDialog'
import { useAgentChat } from '../hooks/useAgentChat'
import { relativeTime, normalizeTimestampMs } from '../utils/format'
import { copyToClipboard } from '../utils/clipboard'
import { AgentPlanCard, AgentQuestionCard, SubAgentActivity } from '../components/AgentInteractions'

function getLastMessagePreview(conv) {
  if (!conv.messages || conv.messages.length === 0) return ''
  for (let i = conv.messages.length - 1; i >= 0; i--) {
    const msg = conv.messages[i]
    if (msg.sender === 'user' || msg.sender === 'agent') {
      return (msg.content || '').slice(0, 40).replace(/\n/g, ' ')
    }
  }
  return ''
}

function stripHtml(html) {
  if (!html) return ''
  return html.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ').trim()
}

function summarizeStatus(text) {
  const plain = stripHtml(text)
  // Extract a short label from "Thinking: ...", "Reasoning: ...", etc.
  const match = plain.match(/^(Thinking|Reasoning|Action taken|Result)[:\s]*/i)
  if (match) return match[1]
  return plain.length > 60 ? plain.slice(0, 60) + '...' : plain
}

function AgentActivityGroup({ items }) {
  const [expanded, setExpanded] = useState(false)
  if (!items || items.length === 0) return null

  const latest = items[items.length - 1]
  const summary = summarizeStatus(latest.content)

  return (
    <div className="chat-message chat-message-assistant">
      <div className="chat-message-avatar chip-neutral">
        <i className="fas fa-cogs" />
      </div>
      <div className="chat-activity-group">
        <button className="chat-activity-toggle" onClick={() => setExpanded(!expanded)}>
          <span className="chat-activity-summary">
            {summary}
            {items.length > 1 && <span className="chat-activity-count">+{items.length - 1}</span>}
          </span>
          <i className={`fas fa-chevron-${expanded ? 'up' : 'down'}`} />
        </button>
        {expanded && (
          <div className="chat-activity-details">
            {items.map((item, idx) => (
              <div key={idx} className="chat-activity-item">
                <span className="chat-activity-item-label">{new Date(item.timestamp).toLocaleTimeString()}</span>
                <div className="chat-activity-item-content wrap-anywhere">{item.content}</div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

export default function AgentChat() {
  const { name } = useParams()
  const { addToast } = useOutletContext()
  const [searchParams] = useSearchParams()
  const userId = searchParams.get('user_id') || undefined

  const {
    conversations, activeConversation, activeId,
    addConversation, switchConversation, deleteConversation,
    deleteAllConversations, renameConversation, addMessage, addMessageToConversation, clearMessages,
    removeMessageFromConversation, updateConversation, upsertInteraction, resolveInteraction, setInteractionError,
  } = useAgentChat(name)

  const messages = activeConversation?.messages || []

  const [input, setInput] = useState('')
  const [canvasMode, setCanvasMode] = useState(false)
  const [canvasOpen, setCanvasOpen] = useState(false)
  const [selectedArtifactId, setSelectedArtifactId] = useState(null)
  const [sidebarOpen, setSidebarOpen] = useState(true)
  const [editingName, setEditingName] = useState(null)
  const [editName, setEditName] = useState('')
  const [chatSearch, setChatSearch] = useState('')
  const [confirmDialog, setConfirmDialog] = useState(null)
  const streamContent = activeConversation?.stream?.content || ''
  const streamReasoning = activeConversation?.stream?.reasoning || ''
  const streamToolCalls = activeConversation?.stream?.toolCalls || []
  const messagesEndRef = useRef(null)
  const messagesRef = useRef(null)
  const textareaRef = useRef(null)
  const stickToBottomRef = useRef(true)
  const eventSourceRef = useRef(null)
  const messageIdCounter = useRef(0)
  const addMessageRef = useRef(addMessage)
  addMessageRef.current = addMessage
  const addMessageToConvRef = useRef(addMessageToConversation)
  addMessageToConvRef.current = addMessageToConversation
  const activeIdRef = useRef(activeId)
  activeIdRef.current = activeId
  const conversationsRef = useRef(conversations)
  conversationsRef.current = conversations
  // Tracks which conversation initiated the current request — SSE responses
  // are pinned to this ID so switching tabs doesn't misdirect them.
  const processingChatIdRef = useRef(null)
  // Maps backend messageID → conversationId for robust SSE routing across navigations.
  const pendingRequestsRef = useRef(new Map())
  const interactionRevisionRef = useRef(0)
  const timelineSequenceRef = useRef(0)
  const nextTimelineOrder = useCallback(() => {
    timelineSequenceRef.current += 1
    return Date.now() * 1000 + timelineSequenceRef.current
  }, [])

  const processing = activeConversation?.status === 'processing'
  const waitingAgents = activeConversation?.status === 'waiting_agents'
  const activeQuestion = (activeConversation?.interactions || []).find(item => item.type === 'question' && !item.resolved)

  const updateConversationRef = useRef(updateConversation)
  updateConversationRef.current = updateConversation
  const upsertInteractionRef = useRef(upsertInteraction)
  upsertInteractionRef.current = upsertInteraction

  const eventConversationId = useCallback((data = {}) => {
    if (data.conversation_id) return data.conversation_id
    const messageId = String(data.message_id || '').replace(/-agent$/, '')
    return pendingRequestsRef.current.get(messageId) || processingChatIdRef.current || activeIdRef.current
  }, [])

  const reconcilePending = useCallback(async (conversationId) => {
    if (!conversationId) return
    const requestRevision = interactionRevisionRef.current
    try {
      const pending = await agentsApi.pending(name, conversationId, userId)
      if (requestRevision !== interactionRevisionRef.current) {
        reconcilePendingRef.current(conversationId)
        return
      }
      const pendingKeys = new Set()
      for (const question of pending?.questions || []) {
        const targetId = question.conversation_id || conversationId
        pendingKeys.add(`question:${question.id}`)
        upsertInteractionRef.current(targetId, { ...question, type: 'question', resolved: false, timelineOrder: nextTimelineOrder() })
        updateConversationRef.current(targetId, { status: 'waiting_user' })
      }
      if (pending?.plan) {
        const targetId = pending.plan.conversation_id || conversationId
        pendingKeys.add(`plan:${pending.plan.id}`)
        upsertInteractionRef.current(targetId, { ...pending.plan, type: 'plan', resolved: false, timelineOrder: nextTimelineOrder() })
        updateConversationRef.current(targetId, { status: 'waiting_user' })
      }
      updateConversationRef.current(conversationId, conversation => {
        const interactions = (conversation.interactions || []).map(item => (
          !item.resolved && !pendingKeys.has(`${item.type}:${item.id}`) &&
          // The API returns all questions but only the oldest pending plan.
          (item.type !== 'plan' || !pending?.plan)
            ? { ...item, resolved: true, resolution: { status: 'no_longer_pending' } }
            : item
        ))
        const hasPending = interactions.some(item => !item.resolved)
        return {
          interactions,
          status: !hasPending && conversation.status === 'waiting_user' ? 'completed' : conversation.status,
        }
      })
    } catch (err) {
      if (err.status === 501) return
      addToast(`Could not recover pending agent input: ${err.message}`, 'error')
    }
  }, [name, userId, addToast, nextTimelineOrder])

  const reconcilePendingRef = useRef(reconcilePending)
  reconcilePendingRef.current = reconcilePending

  const nextId = useCallback(() => {
    messageIdCounter.current += 1
    return `local:${Date.now()}:${messageIdCounter.current}`
  }, [])

  // Connect to SSE endpoint — only reconnect when agent name changes
  useEffect(() => {
    const url = apiUrl(agentsApi.sseUrl(name, userId))
    const es = new EventSource(url)
    eventSourceRef.current = es
    es.addEventListener('open', () => {
      const conversationId = activeIdRef.current
      for (const conversation of conversationsRef.current) {
        if (conversation.status === 'processing') {
          updateConversationRef.current(conversation.id, {
            status: 'completed',
            stream: { content: '', reasoning: '', toolCalls: [] },
          })
        }
      }
      reconcilePendingRef.current(conversationId)
    })

    es.addEventListener('json_message', (e) => {
      try {
        const data = JSON.parse(e.data)
        const sender = data.sender || (data.role === 'user' ? 'user' : 'agent')
        // Skip user message echoes — already added locally in handleSend
        if (sender === 'user') return
        const msg = {
          id: data.id
            ? `server:${data.id}`
            : data.message_id ? `server:${data.message_id}:${sender}` : nextId(),
          sender,
          content: data.content || data.message || '',
          // Backend timestamp encoding varies by deploy mode (RFC3339 string,
          // Unix ms, or Unix ns); normalize to JS milliseconds.
          timestamp: normalizeTimestampMs(data.timestamp),
          timelineOrder: nextTimelineOrder(),
        }
        if (data.metadata && Object.keys(data.metadata).length > 0) {
          msg.metadata = data.metadata
        }
        const msgId = data.message_id || ''
        const baseId = msgId.replace(/-agent$/, '')
        const targetId = eventConversationId(data)
        addMessageToConvRef.current(targetId, msg)
        // Clear streaming + processing state when the final agent message arrives
        if (sender === 'agent') {
          pendingRequestsRef.current.delete(baseId)
          processingChatIdRef.current = null
          updateConversationRef.current(targetId, {
            status: 'completed',
            stream: { content: '', reasoning: '', toolCalls: [] },
          })
        }
      } catch (_err) {
        // ignore malformed messages
      }
    })

    es.addEventListener('json_message_status', (e) => {
      try {
        const data = JSON.parse(e.data)
        const targetId = eventConversationId(data)
        if (data.status === 'processing') {
          // Track which conversation is processing so responses go to the right place.
          // Only set if not already pinned by handleSend (avoids race when user switches conversations).
          if (!processingChatIdRef.current) {
            processingChatIdRef.current = targetId
          }
          updateConversationRef.current(targetId, {
            status: 'processing',
            stream: { content: '', reasoning: '', toolCalls: [] },
          })
        } else if (data.status === 'waiting_user' || data.status === 'waiting_agents') {
          updateConversationRef.current(targetId, { status: data.status })
        } else if (data.status === 'completed') {
          updateConversationRef.current(targetId, { status: 'completed' })
        } else if (data.status === 'failed' || data.status === 'error' || data.status === 'canceled' || data.status === 'cancelled') {
          updateConversationRef.current(targetId, { status: 'completed' })
          reconcilePendingRef.current(targetId)
        }
      } catch (_err) {
        // ignore
      }
    })

    es.addEventListener('stream_event', (e) => {
      try {
        const data = JSON.parse(e.data)
        const targetId = eventConversationId(data)
        if (data.type === 'reasoning') {
          updateConversationRef.current(targetId, conversation => {
            const stream = conversation.stream || { content: '', reasoning: '', toolCalls: [] }
            return { stream: { ...stream, reasoning: stream.reasoning + (data.content || '') } }
          })
        } else if (data.type === 'content') {
          updateConversationRef.current(targetId, conversation => {
            const stream = conversation.stream || { content: '', reasoning: '', toolCalls: [] }
            return { stream: { ...stream, content: stream.content + (data.content || '') } }
          })
        } else if (data.type === 'tool_call') {
          const name = data.tool_name || ''
          const args = data.tool_args || ''
          updateConversationRef.current(targetId, conversation => {
            const stream = conversation.stream || { content: '', reasoning: '', toolCalls: [] }
            const prev = stream.toolCalls || []
            if (name) {
              return { stream: { ...stream, toolCalls: [...prev, { name, args }] } }
            }
            if (prev.length === 0) return {}
            const updated = [...prev]
            updated[updated.length - 1] = { ...updated[updated.length - 1], args: updated[updated.length - 1].args + args }
            return { stream: { ...stream, toolCalls: updated } }
          })
        } else if (data.type === 'tool_result') {
          const tname = data.tool_name || ''
          updateConversationRef.current(targetId, conversation => {
            const stream = conversation.stream || { content: '', reasoning: '', toolCalls: [] }
            const prev = stream.toolCalls || []
            const updated = [...prev]
            const idx = updated.findLastIndex(tc => tc.name === tname && !tc.result)
            if (idx >= 0) {
              updated[idx] = { ...updated[idx], result: data.tool_result || 'done' }
            }
            return { stream: { ...stream, toolCalls: updated } }
          })
        } else if (data.type === 'done') {
          // One agent turn runs several internal LLM generations (tool
          // selection, reasoning, final answer) over the same SSE channel;
          // 'done' marks the boundary between them. Reset the accumulated
          // text so an internal generation's output doesn't merge into the
          // next one's bubble — the final json_message carries the
          // authoritative full answer anyway.
          updateConversationRef.current(targetId, conversation => ({
            stream: { ...(conversation.stream || {}), content: '', reasoning: '' },
          }))
        }
      } catch (_err) {
        // ignore
      }
    })

    es.addEventListener('status', (e) => {
      const text = e.data
      if (!text) return
      let data = {}
      try { data = JSON.parse(text) } catch { data = { message: text } }
      const targetId = eventConversationId(data)
      addMessageToConvRef.current(targetId, {
        id: nextId(),
        sender: 'system',
        content: data.message || data.status || text,
        timestamp: Date.now(),
        timelineOrder: nextTimelineOrder(),
      })
    })

    es.addEventListener('json_error', (e) => {
      let targetId = activeIdRef.current
      try {
        const data = JSON.parse(e.data)
        targetId = eventConversationId(data)
        addToast(data.error || data.message || 'Agent error', 'error')
      } catch (_err) {
        addToast('Agent error', 'error')
      }
      processingChatIdRef.current = null
      updateConversationRef.current(targetId, { status: 'completed' })
      reconcilePendingRef.current(targetId)
    })

    es.onerror = () => {
      addToast('SSE connection lost, attempting to reconnect...', 'warning')
    }

    es.addEventListener('question', (e) => {
      try {
        const data = JSON.parse(e.data)
        const targetId = eventConversationId(data)
        interactionRevisionRef.current += 1
        upsertInteractionRef.current(targetId, { ...data, type: 'question', resolved: false, timelineOrder: nextTimelineOrder() })
        updateConversationRef.current(targetId, { status: 'waiting_user' })
      } catch (_err) { /* ignore malformed messages */ }
    })

    es.addEventListener('plan', (e) => {
      try {
        const data = JSON.parse(e.data)
        const targetId = eventConversationId(data)
        interactionRevisionRef.current += 1
        upsertInteractionRef.current(targetId, { ...data, type: 'plan', resolved: false, timelineOrder: nextTimelineOrder() })
        updateConversationRef.current(targetId, { status: 'waiting_user' })
      } catch (_err) { /* ignore malformed messages */ }
    })

    es.addEventListener('sub_agent', (e) => {
      try {
        const data = JSON.parse(e.data)
        const targetId = eventConversationId(data)
        updateConversationRef.current(targetId, conversation => {
          const current = conversation.subAgents || []
          const index = current.findIndex(item => item.agent_id === data.agent_id)
          const eventTimestamp = normalizeTimestampMs(data.timestamp)
          const timed = {
            ...data,
            timestamp: eventTimestamp,
            ...(data.status === 'spawned' ? { spawned_at: eventTimestamp } : { completed_at: eventTimestamp }),
          }
          if (index < 0) return { subAgents: [...current, timed] }
          const subAgents = [...current]
          subAgents[index] = { ...subAgents[index], ...timed }
          return { subAgents }
        })
      } catch (_err) { /* ignore malformed messages */ }
    })

    return () => {
      es.close()
      eventSourceRef.current = null
      processingChatIdRef.current = null
      pendingRequestsRef.current.clear()
    }
  }, [name, userId, addToast, nextId, nextTimelineOrder, eventConversationId])

  useEffect(() => {
    reconcilePending(activeId)
  }, [activeId, reconcilePending])

  // Track whether the user is pinned to the bottom. If they scroll up
  // while a response is streaming, stop forcing them back down.
  useEffect(() => {
    const el = messagesRef.current
    if (!el) return
    const onScroll = () => {
      const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
      stickToBottomRef.current = distanceFromBottom < 80
    }
    el.addEventListener('scroll', onScroll, { passive: true })
    return () => el.removeEventListener('scroll', onScroll)
  }, [])

  // Auto-scroll only when the user hasn't scrolled away from the bottom.
  useEffect(() => {
    if (!stickToBottomRef.current) return
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, streamContent, streamReasoning, streamToolCalls])

  // When switching conversations, snap to bottom and re-pin.
  useEffect(() => {
    stickToBottomRef.current = true
    messagesEndRef.current?.scrollIntoView({ behavior: 'auto' })
  }, [activeId])

  // Highlight code blocks + add per-block copy buttons (parity with Chat). A
  // MutationObserver on the messages container fires reliably for streamed and
  // loaded messages; it disconnects while mutating so its own edits do not
  // retrigger it.
  useEffect(() => {
    const el = messagesRef.current
    if (!el) return
    let obs
    const run = () => {
      obs?.disconnect()
      highlightAll(el)
      enhanceCodeBlocks(el)
      obs?.observe(el, { childList: true, subtree: true })
    }
    obs = new MutationObserver(run)
    run()
    return () => obs.disconnect()
  }, [])

  const agentMessages = useMemo(() => messages.filter(m => m.sender === 'agent'), [messages])
  const codeArtifacts = useMemo(
    () => canvasMode ? extractCodeArtifacts(agentMessages, 'sender', 'agent') : [],
    [agentMessages, canvasMode]
  )
  const metaArtifacts = useMemo(
    () => canvasMode ? extractMetadataArtifacts(messages, name) : [],
    [messages, canvasMode, name]
  )
  const artifacts = useMemo(() => [...codeArtifacts, ...metaArtifacts], [codeArtifacts, metaArtifacts])

  const prevArtifactCountRef = useRef(0)
  useEffect(() => {
    prevArtifactCountRef.current = artifacts.length
  }, [activeId])
  useEffect(() => {
    if (artifacts.length > prevArtifactCountRef.current && artifacts.length > 0) {
      setSelectedArtifactId(artifacts[artifacts.length - 1].id)
      if (!canvasOpen) setCanvasOpen(true)
    }
    prevArtifactCountRef.current = artifacts.length
  }, [artifacts])

  // Event delegation for artifact cards
  useEffect(() => {
    const el = messagesRef.current
    if (!el || !canvasMode) return
    const handler = (e) => {
      const openBtn = e.target.closest('.artifact-card-open')
      const downloadBtn = e.target.closest('.artifact-card-download')
      const card = e.target.closest('.artifact-card')
      if (downloadBtn) {
        e.stopPropagation()
        const id = downloadBtn.dataset.artifactId
        const artifact = artifacts.find(a => a.id === id)
        if (artifact?.code) {
          const blob = new Blob([artifact.code], { type: 'text/plain' })
          const url = URL.createObjectURL(blob)
          const a = document.createElement('a')
          a.href = url
          a.download = artifact.title || 'download.txt'
          a.click()
          URL.revokeObjectURL(url)
        }
        return
      }
      if (openBtn || card) {
        const id = (openBtn || card).dataset.artifactId
        if (id) {
          setSelectedArtifactId(id)
          setCanvasOpen(true)
        }
      }
    }
    el.addEventListener('click', handler)
    return () => el.removeEventListener('click', handler)
  }, [canvasMode, artifacts])

  const openArtifactById = useCallback((id) => {
    setSelectedArtifactId(id)
    setCanvasOpen(true)
  }, [])

  const handleAnswer = useCallback(async (interaction, answer) => {
    setInteractionError(activeId, 'question', interaction.id, '')
    updateConversation(activeId, { status: 'processing' })
    try {
      await agentsApi.answer(name, {
        question_id: interaction.id,
        selected: answer.selected,
        text: answer.text,
      }, userId)
      interactionRevisionRef.current += 1
      resolveInteraction(activeId, 'question', interaction.id, answer)
    } catch (err) {
      setInteractionError(activeId, 'question', interaction.id, err.message)
      updateConversation(activeId, conversation => ({
        status: conversation.status === 'processing' ? 'waiting_user' : conversation.status,
      }))
      throw err
    }
  }, [activeId, name, userId, resolveInteraction, setInteractionError, updateConversation])

  const handlePlanDecision = useCallback(async (interaction, decision) => {
    setInteractionError(activeId, 'plan', interaction.id, '')
    const optimisticStatus = decision.resolution === 'rejected' ? 'completed' : 'processing'
    updateConversation(activeId, { status: optimisticStatus })
    try {
      await agentsApi.decidePlan(name, {
        plan_id: interaction.id,
        approved: decision.approved,
        subtasks: decision.subtasks,
        feedback: decision.feedback,
      }, userId)
      interactionRevisionRef.current += 1
      resolveInteraction(activeId, 'plan', interaction.id, {
        status: decision.resolution,
        feedback: decision.feedback,
        subtasks: decision.subtasks,
      })
      reconcilePendingRef.current(activeId)
    } catch (err) {
      setInteractionError(activeId, 'plan', interaction.id, err.message)
      updateConversation(activeId, conversation => ({
        status: conversation.status === optimisticStatus ? 'waiting_user' : conversation.status,
      }))
      throw err
    }
  }, [activeId, name, userId, resolveInteraction, setInteractionError, updateConversation])

  const handleSend = useCallback(async () => {
    const msg = input.trim()
    if (!msg || processing) return
    setInput('')
    if (textareaRef.current) textareaRef.current.style.height = 'auto'
    // Add user message locally immediately (like standard chat)
    const localMessageId = nextId()
    addMessage({ id: localMessageId, sender: 'user', content: msg, timestamp: Date.now(), timelineOrder: nextTimelineOrder() })
    if (activeQuestion?.allow_free_text) {
      try {
        await handleAnswer(activeQuestion, { selected: [], text: msg })
      } catch (_err) {
        removeMessageFromConversation(activeId, localMessageId)
        setInput(msg)
      }
      return
    }
    updateConversation(activeId, { status: 'processing', stream: { content: '', reasoning: '', toolCalls: [] } })
    processingChatIdRef.current = activeId
    try {
      const resp = await agentsApi.chat(name, msg, activeId, userId)
      // Map backend messageID → conversation so SSE events route correctly
      if (resp && resp.message_id) {
        pendingRequestsRef.current.set(resp.message_id, activeId)
      }
    } catch (err) {
      removeMessageFromConversation(activeId, localMessageId)
      setInput(msg)
      processingChatIdRef.current = null
      if (err.status === 409 && err.body?.pending_question_id) {
        await reconcilePending(activeId)
        requestAnimationFrame(() => messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' }))
      } else {
        addToast(`Failed to send message: ${err.message}`, 'error')
        updateConversation(activeId, { status: 'completed' })
      }
    }
  }, [input, processing, activeQuestion, handleAnswer, name, activeId, addToast, userId, addMessage, nextId, nextTimelineOrder, removeMessageFromConversation, reconcilePending, updateConversation])

  const handleKeyDown = (e) => {
    if (
      e.key === 'Enter' &&
      !e.shiftKey &&
      !e.ctrlKey &&
      !e.metaKey &&
      !e.altKey &&
      !e.nativeEvent?.isComposing &&
      e.keyCode !== 229
    ) {
      e.preventDefault()
      handleSend()
    }
  }

  const copyMessage = async (content) => {
    const ok = await copyToClipboard(content)
    addToast(
      ok ? 'Copied to clipboard' : 'Could not copy to clipboard',
      ok ? 'success' : 'error',
      ok ? 2000 : 3000,
    )
  }

  const senderToRole = (sender) => {
    if (sender === 'agent') return 'assistant'
    if (sender === 'user') return 'user'
    return 'system'
  }

  const startRename = (id, currentName) => {
    setEditingName(id)
    setEditName(currentName)
  }

  const finishRename = () => {
    if (editingName && editName.trim()) {
      renameConversation(editingName, editName.trim())
    }
    setEditingName(null)
  }

  const filteredConversations = chatSearch.trim()
    ? conversations.filter(c => {
      const q = chatSearch.toLowerCase()
      if ((c.name || '').toLowerCase().includes(q)) return true
      return c.messages?.some(m => {
        return (m.content || '').toLowerCase().includes(q)
      })
    })
    : conversations

  return (
    <div className={`chat-layout${sidebarOpen ? '' : ' chat-sidebar-collapsed'}`}>
      {/* Conversation sidebar */}
      <div className={`chat-sidebar${sidebarOpen ? '' : ' hidden'}`}>
        <div className="chat-sidebar-header">
          <button className="btn btn-primary btn-sm flex-1" onClick={() => addConversation()}>
            <i className="fas fa-plus" /> New Chat
          </button>
          <button
            className="btn btn-secondary btn-sm"
            onClick={() => {
              setConfirmDialog({
                title: 'Delete All Conversations',
                message: 'Delete all conversations? This cannot be undone.',
                confirmLabel: 'Delete All',
                danger: true,
                onConfirm: () => { setConfirmDialog(null); deleteAllConversations() },
              })
            }}
            title="Delete all conversations"
            style={{ padding: '6px 8px' }}
          >
            <i className="fas fa-trash" />
          </button>
        </div>

        <div style={{ padding: '0 var(--spacing-sm)' }}>
          <div className="chat-search-wrapper">
            <i className="fas fa-search chat-search-icon" />
            <input
              className="chat-search-input"
              type="text"
              value={chatSearch}
              onChange={(e) => setChatSearch(e.target.value)}
              placeholder="Search conversations..."
            />
            {chatSearch && (
              <button className="chat-search-clear" onClick={() => setChatSearch('')}>
                <i className="fas fa-times" />
              </button>
            )}
          </div>
        </div>

        <div className="chat-list">
          {filteredConversations.map(conv => (
            <div
              key={conv.id}
              className={`chat-list-item ${conv.id === activeId ? 'active' : ''}`}
              onClick={() => switchConversation(conv.id)}
            >
              <i className="fas fa-message" style={{ fontSize: '0.7rem', flexShrink: 0, marginTop: '2px' }} />
              {editingName === conv.id ? (
                <input
                  className="input"
                  value={editName}
                  onChange={(e) => setEditName(e.target.value)}
                  onBlur={finishRename}
                  onKeyDown={(e) => e.key === 'Enter' && finishRename()}
                  autoFocus
                  onClick={(e) => e.stopPropagation()}
                  style={{ padding: '2px 4px', fontSize: '0.8125rem' }}
                />
              ) : (
                <div className="chat-list-item-info">
                  <div className="chat-list-item-top">
                    <span
                      className="chat-list-item-name"
                      onDoubleClick={() => startRename(conv.id, conv.name)}
                    >
                      {(conv.status === 'processing' || conv.status === 'waiting_agents') && <i className="fas fa-circle-notch fa-spin chat-conversation-spinner" />}
                      {conv.name}
                    </span>
                    <span className="chat-list-item-time">{relativeTime(conv.updatedAt)}</span>
                  </div>
                  <span className="chat-list-item-preview">
                    {getLastMessagePreview(conv) || 'No messages yet'}
                  </span>
                </div>
              )}
              <div className="chat-list-item-actions">
                <button
                  onClick={(e) => { e.stopPropagation(); startRename(conv.id, conv.name) }}
                  title="Rename"
                >
                  <i className="fas fa-edit" />
                </button>
                {conversations.length > 1 && (
                  <button
                    className="chat-list-item-delete"
                    onClick={(e) => { e.stopPropagation(); deleteConversation(conv.id) }}
                    title="Delete conversation"
                  >
                    <i className="fas fa-trash" />
                  </button>
                )}
              </div>
            </div>
          ))}
          {filteredConversations.length === 0 && chatSearch && (
            <div style={{ padding: 'var(--spacing-sm)', textAlign: 'center', color: 'var(--color-text-muted)', fontSize: '0.8rem' }}>
              No conversations match your search
            </div>
          )}
        </div>
      </div>

    <div className="chat-main">
      {/* Header */}
      <div className="chat-header">
        <button
          className="btn btn-secondary btn-sm"
          onClick={() => setSidebarOpen(prev => !prev)}
          title={sidebarOpen ? 'Hide chat list' : 'Show chat list'}
          style={{ flexShrink: 0 }}
        >
          <i className={`fas fa-${sidebarOpen ? 'angles-left' : 'angles-right'}`} />
        </button>
        <span className="chat-header-title">
          <i className="fas fa-robot icon-before" />
          {name}
        </span>
        <div className="chat-header-actions">
          <label className="canvas-mode-toggle" title="Extract code blocks and media into a side panel for preview, copy, and download">
            <i className="fas fa-columns" />
            <span className="canvas-mode-label">Canvas</span>
            <span className={`toggle${canvasMode ? ' toggle--on' : ''}`}>
              <input
                type="checkbox"
                checked={canvasMode}
                onChange={(e) => {
                  setCanvasMode(e.target.checked)
                  if (!e.target.checked) setCanvasOpen(false)
                }}
              />
              <span className="toggle__track">
                <span className="toggle__thumb" />
              </span>
            </span>
          </label>
          {canvasMode && artifacts.length > 0 && !canvasOpen && (
            <button
              className="btn btn-secondary btn-sm"
              onClick={() => { setSelectedArtifactId(artifacts[0]?.id); setCanvasOpen(true) }}
              title="Open canvas panel"
            >
              <i className="fas fa-layer-group" /> {artifacts.length}
            </button>
          )}
          <a
            className="btn btn-secondary btn-sm"
            href={`/app/agents/${encodeURIComponent(name)}/status${userId ? `?user_id=${encodeURIComponent(userId)}` : ''}`}
            target="_blank"
            rel="noopener noreferrer"
            title="View status & observables in a new tab"
          >
            <i className="fas fa-chart-bar" /> Status
          </a>
          <button className="btn btn-secondary btn-sm" onClick={() => clearMessages()} disabled={messages.length === 0} title="Clear chat history">
            <i className="fas fa-eraser" /> Clear
          </button>
        </div>
      </div>

      {/* Messages */}
      <div className="chat-messages" ref={messagesRef}>
        {messages.length === 0 && (activeConversation?.interactions || []).length === 0 && !processing && (
          <div className="chat-empty-state">
            <div className="chat-empty-icon">
              <i className="fas fa-robot" />
            </div>
            <h2 className="chat-empty-title">Chat with {name}</h2>
            <p className="chat-empty-text">Send a message to start a conversation with this agent.</p>
            <div className="chat-empty-hints">
              <span><i className="fas fa-keyboard" /> Enter to send</span>
              <span><i className="fas fa-level-down-alt" /> Shift+Enter for newline</span>
            </div>
          </div>
        )}
        {(() => {
          const elements = []
          let systemBuf = []
          const flushSystem = (key) => {
            if (systemBuf.length > 0) {
              elements.push(<AgentActivityGroup key={`sag-${key}`} items={[...systemBuf]} />)
              systemBuf = []
            }
          }
          const transcript = [
            ...messages.map((message, index) => ({ kind: 'message', value: message, index })),
            ...(activeConversation?.interactions || []).map((interaction, index) => ({
              kind: 'interaction', value: interaction, index: messages.length + index,
            })),
          ].sort((a, b) => {
            const timeDelta = (a.value.timelineOrder ?? normalizeTimestampMs(a.value.timestamp))
              - (b.value.timelineOrder ?? normalizeTimestampMs(b.value.timestamp))
            return timeDelta || a.index - b.index
          })
          transcript.forEach((entry, transcriptIndex) => {
            if (entry.kind === 'interaction') {
              flushSystem(`interaction-${entry.value.id}`)
              elements.push(entry.value.type === 'question'
                ? <AgentQuestionCard key={`question-${entry.value.id}`} interaction={entry.value} onAnswer={handleAnswer} />
                : <AgentPlanCard key={`plan-${entry.value.id}`} interaction={entry.value} onDecide={handlePlanDecision} />)
              return
            }
            const msg = entry.value
            const idx = entry.index
            const role = senderToRole(msg.sender)
            if (role === 'system') {
              systemBuf.push(msg)
              return
            }
            flushSystem(transcriptIndex)
            elements.push(
              <div key={msg.id} className={`chat-message chat-message-${role}`}>
                <div className="chat-message-avatar">
                  <i className={`fas ${role === 'user' ? 'fa-user' : 'fa-robot'}`} />
                </div>
                <div className="chat-message-bubble">
                  <div className="chat-message-content">
                    {role === 'user' ? (
                      <div className="wrap-anywhere">{msg.content}</div>
                    ) : (
                      <div dangerouslySetInnerHTML={{
                        __html: canvasMode
                          ? renderMarkdownWithArtifacts(msg.content, idx)
                          : renderMarkdown(msg.content)
                      }} />
                    )}
                  </div>
                  {role === 'assistant' && msg.metadata && (
                    <ResourceCards
                      metadata={msg.metadata}
                      messageIndex={idx}
                      agentName={name}
                      onOpenArtifact={openArtifactById}
                    />
                  )}
                  <div className="chat-message-actions">
                    <button onClick={() => copyMessage(msg.content)} title="Copy">
                      <i className="fas fa-copy" />
                    </button>
                  </div>
                  <div className="chat-message-timestamp">
                    {new Date(msg.timestamp).toLocaleTimeString()}
                  </div>
                </div>
              </div>
            )
          })
          flushSystem('end')
          return elements
        })()}
        {(activeConversation?.subAgents || []).length > 0 && (
          <div className="sub-agent-strip" aria-label="Sub-agent activity">
            {(activeConversation.subAgents || []).map(activity => (
              <SubAgentActivity key={activity.agent_id} activity={activity} />
            ))}
          </div>
        )}
        {(streamReasoning || streamContent || streamToolCalls.length > 0) && (
          <div className="chat-message chat-message-assistant">
            <div className="chat-message-avatar">
              <i className="fas fa-robot" />
            </div>
            <div className="chat-message-bubble">
              {streamReasoning && (
                <details className="chat-activity-group" open={!streamContent} style={{ marginBottom: streamContent ? 'var(--spacing-sm)' : 0 }}>
                  <summary className="chat-activity-toggle clickable">
                    <span className={`chat-activity-summary${!streamContent ? ' chat-activity-shimmer' : ''}`}>
                      {streamContent ? 'Thinking' : 'Thinking...'}
                    </span>
                  </summary>
                  <div className="chat-activity-details">
                    <div className="chat-activity-item chat-activity-thinking">
                      <div className="chat-activity-item-content chat-activity-live"
                        dangerouslySetInnerHTML={{ __html: renderMarkdown(streamReasoning) }} />
                    </div>
                  </div>
                </details>
              )}
              {streamToolCalls.length > 0 && (
                <div className="chat-activity-group mb-sm">
                  {streamToolCalls.map((tc, idx) => (
                    <details key={idx} className="chat-activity-item chat-activity-tool-call" style={{ padding: 'var(--spacing-xs) var(--spacing-sm)' }} open={!tc.result}>
                      <summary className="chat-activity-item-label" style={{ cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 'var(--spacing-xs)' }}>
                        <i className={`fas ${tc.result ? 'fa-check' : 'fa-bolt'}`} />
                        <strong>{tc.name}</strong>
                        <span style={{ opacity: 0.5, fontSize: '0.85em' }}>
                          {tc.result ? 'done' : 'calling...'}
                        </span>
                      </summary>
                      {tc.args && (
                        <pre style={{ margin: '4px 0', fontSize: '0.75rem', opacity: 0.8, whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                          {(() => { try { return JSON.stringify(JSON.parse(tc.args), null, 2) } catch { return tc.args } })()}
                        </pre>
                      )}
                      {tc.result && (
                        <pre style={{ margin: '4px 0', fontSize: '0.75rem', opacity: 0.7, whiteSpace: 'pre-wrap', wordBreak: 'break-word', maxHeight: '200px', overflow: 'auto' }}>
                          {tc.result}
                        </pre>
                      )}
                    </details>
                  ))}
                </div>
              )}
              {streamContent && (
                <div className="chat-message-content">
                  <span dangerouslySetInnerHTML={{ __html: renderMarkdown(streamContent) }} />
                  <span className="chat-streaming-cursor" />
                </div>
              )}
            </div>
          </div>
        )}
        {(processing || waitingAgents) && !streamReasoning && !streamContent && streamToolCalls.length === 0 && (
          <div className="chat-message chat-message-assistant">
            <div className="chat-message-avatar chip-neutral">
              <i className="fas fa-cogs" />
            </div>
            <div className="chat-activity-group chat-activity-streaming">
              <div className="chat-activity-toggle" style={{ cursor: 'default' }}>
                <span className="chat-activity-summary chat-activity-shimmer">Working...</span>
              </div>
            </div>
          </div>
        )}
        <div ref={messagesEndRef} />
      </div>

      {/* Input area */}
      <div className="chat-input-area">
        <div className="chat-input-wrapper">
          <textarea
            ref={textareaRef}
            className="chat-input"
            value={input}
            onChange={(e) => {
              setInput(e.target.value)
              const ta = e.target
              ta.style.height = 'auto'
              ta.style.height = Math.min(ta.scrollHeight, 150) + 'px'
            }}
            onKeyDown={handleKeyDown}
            placeholder="Type a message..."
            disabled={processing}
            rows={1}
          />
          <button
            className="chat-send-btn"
            onClick={handleSend}
            disabled={processing || !input.trim()}
            aria-label="Send message"
            title="Send message"
          >
            <i className="fas fa-paper-plane" aria-hidden="true" />
          </button>
        </div>
      </div>
    </div>
    {canvasOpen && artifacts.length > 0 && (
      <CanvasPanel
        artifacts={artifacts}
        selectedId={selectedArtifactId}
        onSelect={setSelectedArtifactId}
        onClose={() => setCanvasOpen(false)}
      />
    )}
    <ConfirmDialog
      open={!!confirmDialog}
      title={confirmDialog?.title}
      message={confirmDialog?.message}
      confirmLabel={confirmDialog?.confirmLabel}
      danger={confirmDialog?.danger}
      onConfirm={confirmDialog?.onConfirm}
      onCancel={() => setConfirmDialog(null)}
    />
    </div>
  )
}
