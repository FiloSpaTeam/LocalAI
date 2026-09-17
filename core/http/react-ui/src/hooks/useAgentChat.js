import { useState, useCallback, useEffect } from 'react'
import { generateId } from '../utils/format'
import { useDebouncedEffect } from './useDebounce'

const STORAGE_KEY_PREFIX = 'localai_agent_chats_'

function storageKey(agentName) {
  return STORAGE_KEY_PREFIX + agentName
}

function loadConversations(agentName) {
  try {
    const stored = localStorage.getItem(storageKey(agentName))
    if (stored) {
      const data = JSON.parse(stored)
      if (data && Array.isArray(data.conversations)) {
        return {
          ...data,
          conversations: data.conversations.map(conversation => (
            conversation.status === 'processing'
              ? { ...conversation, status: 'completed', stream: { content: '', reasoning: '', toolCalls: [] } }
              : conversation
          )),
        }
      }
    }
  } catch (_e) {
    localStorage.removeItem(storageKey(agentName))
  }
  return null
}

function saveConversations(agentName, conversations, activeId) {
  try {
    const data = {
      conversations: conversations.map(c => ({
        id: c.id,
        name: c.name,
        messages: c.messages,
        createdAt: c.createdAt,
        updatedAt: c.updatedAt,
        interactions: c.interactions || [],
        status: c.status || 'completed',
        stream: c.stream || { content: '', reasoning: '', toolCalls: [] },
        subAgents: c.subAgents || [],
      })),
      activeId,
      lastSaved: Date.now(),
    }
    localStorage.setItem(storageKey(agentName), JSON.stringify(data))
  } catch (err) {
    if (err.name === 'QuotaExceededError' || err.code === 22) {
      console.warn('localStorage quota exceeded for agent chats')
    }
  }
}

function createConversation() {
  return {
    id: generateId(),
    name: 'New Chat',
    messages: [],
    createdAt: Date.now(),
    updatedAt: Date.now(),
    interactions: [],
    status: 'completed',
    stream: { content: '', reasoning: '', toolCalls: [] },
    subAgents: [],
  }
}

export function useAgentChat(agentName) {
  const [conversations, setConversations] = useState(() => {
    const stored = loadConversations(agentName)
    if (stored && stored.conversations.length > 0) return stored.conversations
    return [createConversation()]
  })

  const [activeId, setActiveId] = useState(() => {
    const stored = loadConversations(agentName)
    if (stored && stored.activeId) return stored.activeId
    return conversations[0]?.id
  })

  const activeConversation = conversations.find(c => c.id === activeId) || conversations[0]

  useDebouncedEffect(() => saveConversations(agentName, conversations, activeId), [agentName, conversations, activeId])

  // Save immediately on unmount
  useEffect(() => {
    return () => {
      saveConversations(agentName, conversations, activeId)
    }
  }, [agentName, conversations, activeId])

  const addConversation = useCallback(() => {
    const conv = createConversation()
    setConversations(prev => [conv, ...prev])
    setActiveId(conv.id)
    return conv
  }, [])

  const switchConversation = useCallback((id) => {
    setActiveId(id)
  }, [])

  const deleteConversation = useCallback((id) => {
    setConversations(prev => {
      if (prev.length <= 1) return prev
      const filtered = prev.filter(c => c.id !== id)
      const newActiveId = id === activeId && filtered.length > 0 ? filtered[0].id : activeId
      if (id === activeId) {
        setActiveId(newActiveId)
      }
      saveConversations(agentName, filtered, newActiveId)
      return filtered
    })
  }, [activeId, agentName])

  const deleteAllConversations = useCallback(() => {
    const conv = createConversation()
    setConversations([conv])
    setActiveId(conv.id)
    saveConversations(agentName, [conv], conv.id)
  }, [agentName])

  const renameConversation = useCallback((id, name) => {
    setConversations(prev => prev.map(c =>
      c.id === id ? { ...c, name, updatedAt: Date.now() } : c
    ))
  }, [])

  const addMessage = useCallback((msg) => {
    setConversations(prev => prev.map(c => {
      if (c.id !== activeId) return c
      if (c.messages.some(existing => existing.id === msg.id)) return c
      const updated = {
        ...c,
        messages: [...c.messages, msg],
        updatedAt: Date.now(),
      }
      // Auto-name from first user message
      if (c.messages.length === 0 && msg.sender === 'user') {
        const text = msg.content || ''
        updated.name = text.slice(0, 40) + (text.length > 40 ? '...' : '')
      }
      return updated
    }))
  }, [activeId])

  // Add a message to a specific conversation by ID, regardless of which is active.
  // Used by SSE handlers to pin responses to the conversation that initiated the request.
  const addMessageToConversation = useCallback((conversationId, msg) => {
    setConversations(prev => prev.map(c => {
      if (c.id !== conversationId) return c
      if (c.messages.some(existing => existing.id === msg.id)) return c
      const updated = {
        ...c,
        messages: [...c.messages, msg],
        updatedAt: Date.now(),
      }
      if (c.messages.length === 0 && msg.sender === 'user') {
        const text = msg.content || ''
        updated.name = text.slice(0, 40) + (text.length > 40 ? '...' : '')
      }
      return updated
    }))
  }, [])

  const removeMessageFromConversation = useCallback((conversationId, messageId) => {
    setConversations(prev => prev.map(c => (
      c.id === conversationId
        ? { ...c, messages: c.messages.filter(message => message.id !== messageId), updatedAt: Date.now() }
        : c
    )))
  }, [])

  const updateConversation = useCallback((conversationId, updater) => {
    setConversations(prev => prev.map(c => {
      if (c.id !== conversationId) return c
      const changes = typeof updater === 'function' ? updater(c) : updater
      return { ...c, ...changes, updatedAt: Date.now() }
    }))
  }, [])

  const upsertInteraction = useCallback((conversationId, interaction) => {
    updateConversation(conversationId, conversation => {
      const current = conversation.interactions || []
      const index = current.findIndex(item => item.id === interaction.id && item.type === interaction.type)
      if (index < 0) return { interactions: [...current, interaction] }
      const interactions = [...current]
      interactions[index] = interactions[index].resolved
        ? { ...interaction, ...interactions[index] }
        : {
          ...interactions[index],
          ...interaction,
          timelineOrder: interactions[index].timelineOrder ?? interaction.timelineOrder,
        }
      return { interactions }
    })
  }, [updateConversation])

  const resolveInteraction = useCallback((conversationId, type, id, resolution) => {
    updateConversation(conversationId, conversation => ({
      interactions: (conversation.interactions || []).map(item => (
        item.id === id && item.type === type
          ? { ...item, ...(resolution.subtasks ? { subtasks: resolution.subtasks } : {}), resolved: true, resolution, error: '' }
          : item
      )),
    }))
  }, [updateConversation])

  const setInteractionError = useCallback((conversationId, type, id, error) => {
    updateConversation(conversationId, conversation => ({
      interactions: (conversation.interactions || []).map(item => (
        item.id === id && item.type === type ? { ...item, error } : item
      )),
    }))
  }, [updateConversation])

  const clearMessages = useCallback(() => {
    setConversations(prev => {
      const updated = prev.map(c =>
        c.id === activeId ? {
          ...c,
          messages: [],
          interactions: [],
          subAgents: [],
          status: 'completed',
          stream: { content: '', reasoning: '', toolCalls: [] },
          updatedAt: Date.now(),
        } : c
      )
      // Save immediately so a page refresh doesn't restore the old messages
      saveConversations(agentName, updated, activeId)
      return updated
    })
  }, [activeId, agentName])

  const getMessages = useCallback(() => {
    return activeConversation?.messages || []
  }, [activeConversation])

  return {
    conversations,
    activeConversation,
    activeId,
    addConversation,
    switchConversation,
    deleteConversation,
    deleteAllConversations,
    renameConversation,
    addMessage,
    addMessageToConversation,
    removeMessageFromConversation,
    updateConversation,
    upsertInteraction,
    resolveInteraction,
    setInteractionError,
    clearMessages,
    getMessages,
  }
}
