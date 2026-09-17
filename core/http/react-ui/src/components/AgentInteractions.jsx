// SPDX-License-Identifier: MIT

import { useEffect, useState } from 'react'

export function AgentQuestionCard({ interaction, onAnswer }) {
  const [text, setText] = useState(interaction.resolution?.text || '')
  const [submitting, setSubmitting] = useState(false)
  const disabled = interaction.resolved || submitting

  const submit = async (option = '') => {
    if (!option && !text.trim()) return
    setSubmitting(true)
    try {
      await onAnswer(interaction, { selected: option ? [option] : [], text: option ? '' : text.trim() })
    } catch (_err) {
      // The persisted interaction error is rendered below the form.
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <section className="agent-interaction-card" role="group" aria-label={`Question: ${interaction.question}`}>
      <div className="agent-interaction-heading">
        <span className="agent-interaction-kicker"><i className="fas fa-circle-question" /> Input needed</span>
        <span className="agent-interaction-agent">{interaction.agent_id}</span>
      </div>
      <p className="agent-interaction-title">{interaction.question}</p>
      {interaction.options?.length > 0 && (
        <div className="agent-question-options">
          {interaction.options.map(option => (
            <button
              key={option}
              className="agent-question-option"
              disabled={disabled}
              onClick={() => submit(option)}
            >
              {option}
            </button>
          ))}
        </div>
      )}
      {interaction.allow_free_text && (
        <textarea
          className="input agent-interaction-textarea"
          aria-label="Free-text answer"
          value={text}
          disabled={disabled}
          onChange={event => setText(event.target.value)}
          placeholder="Write an answer"
          rows={2}
        />
      )}
      {interaction.error && <p className="agent-interaction-error" role="alert">{interaction.error}</p>}
      <div className="agent-interaction-actions">
        {interaction.resolved ? (
          <button className="btn btn-secondary btn-sm" disabled>
            {interaction.resolution?.status === 'no_longer_pending' ? 'No longer pending' : 'Answered'}
          </button>
        ) : interaction.allow_free_text ? (
          <button className="btn btn-primary btn-sm" disabled={disabled || !text.trim()} onClick={() => submit('')}>
            {submitting ? 'Submitting…' : 'Submit answer'}
          </button>
        ) : null}
      </div>
    </section>
  )
}

export function AgentPlanCard({ interaction, onDecide }) {
  const [subtasks, setSubtasks] = useState(interaction.subtasks || [])
  const [feedback, setFeedback] = useState(interaction.resolution?.feedback || '')
  const [submitting, setSubmitting] = useState(false)
  const disabled = interaction.resolved || submitting

  const updateSubtask = (index, value) => setSubtasks(items => items.map((item, i) => i === index ? value : item))
  const moveSubtask = (index, delta) => setSubtasks(items => {
    const target = index + delta
    if (target < 0 || target >= items.length) return items
    const next = [...items]
    ;[next[index], next[target]] = [next[target], next[index]]
    return next
  })
  const decide = async (approved, resolution) => {
    setSubmitting(true)
    try {
      await onDecide(interaction, {
        approved,
        subtasks: subtasks.map(item => item.trim()).filter(Boolean),
        feedback: resolution === 'rejected' ? '' : feedback.trim(),
        resolution,
      })
    } catch (_err) {
      // The persisted interaction error is rendered below the form.
    } finally {
      setSubmitting(false)
    }
  }
  const resolvedLabel = interaction.resolution?.status === 'approved'
    ? 'Approved'
    : interaction.resolution?.status === 'rejected'
      ? 'Rejected'
      : interaction.resolution?.status === 'no_longer_pending' ? 'No longer pending' : 'Changes requested'

  return (
    <section className="agent-interaction-card agent-plan-card" role="group" aria-label={`Plan: ${interaction.description}`}>
      <div className="agent-interaction-heading">
        <span className="agent-interaction-kicker"><i className="fas fa-list-check" /> Plan approval</span>
        <span className="agent-interaction-agent">{interaction.agent_id}</span>
      </div>
      <p className="agent-interaction-title">{interaction.description}</p>
      <ol className="agent-plan-list">
        {subtasks.map((subtask, index) => (
          <li key={`${interaction.id}-${index}`} className="agent-plan-item">
            <input
              className="input agent-plan-input"
              aria-label={`Subtask ${index + 1}`}
              value={subtask}
              disabled={disabled}
              onChange={event => updateSubtask(index, event.target.value)}
            />
            <div className="agent-plan-controls">
              <button className="btn btn-secondary btn-sm" aria-label={`Move subtask ${index + 1} up`} disabled={disabled || index === 0} onClick={() => moveSubtask(index, -1)}><i className="fas fa-arrow-up" /></button>
              <button className="btn btn-secondary btn-sm" aria-label={`Move subtask ${index + 1} down`} disabled={disabled || index === subtasks.length - 1} onClick={() => moveSubtask(index, 1)}><i className="fas fa-arrow-down" /></button>
              <button className="btn btn-secondary btn-sm" aria-label={`Remove subtask ${index + 1}`} disabled={disabled} onClick={() => setSubtasks(items => items.filter((_, i) => i !== index))}><i className="fas fa-xmark" /></button>
            </div>
          </li>
        ))}
      </ol>
      <button className="btn btn-secondary btn-sm" disabled={disabled} onClick={() => setSubtasks(items => [...items, ''])}><i className="fas fa-plus" /> Add subtask</button>
      <textarea
        className="input agent-interaction-textarea"
        aria-label="Decision feedback"
        value={feedback}
        disabled={disabled}
        onChange={event => setFeedback(event.target.value)}
        placeholder="Feedback for changes or rejection"
        rows={2}
      />
      {interaction.error && <p className="agent-interaction-error" role="alert">{interaction.error}</p>}
      <div className="agent-interaction-actions">
        {interaction.resolved ? (
          <button className="btn btn-secondary btn-sm" disabled>{resolvedLabel}</button>
        ) : (
          <>
            <button className="btn btn-primary btn-sm" disabled={disabled || subtasks.every(item => !item.trim())} onClick={() => decide(true, 'approved')}>Approve plan</button>
            <button className="btn btn-secondary btn-sm" disabled={disabled || !feedback.trim()} onClick={() => decide(false, 'changes_requested')}>Request changes</button>
            <button className="btn btn-danger btn-sm" disabled={disabled} onClick={() => decide(false, 'rejected')}>Reject plan</button>
          </>
        )}
      </div>
    </section>
  )
}

export function SubAgentActivity({ activity, now = Date.now() }) {
  const [expanded, setExpanded] = useState(false)
  const [clock, setClock] = useState(now)
  const running = activity.status === 'spawned'
  useEffect(() => {
    if (!running) return undefined
    const timer = setInterval(() => setClock(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [running])
  const startedAt = Number(activity.spawned_at || activity.timestamp || now)
  const endedAt = activity.status === 'completed' || activity.status === 'failed'
    ? Number(activity.completed_at || activity.timestamp || now)
    : clock
  const elapsed = Math.max(0, endedAt - startedAt)
  const elapsedLabel = elapsed < 1000 ? '<1s' : `${Math.floor(elapsed / 1000)}s`
  return (
    <div className="sub-agent-activity">
      <button
        className="sub-agent-activity-toggle"
        aria-expanded={expanded}
        aria-label={`${activity.agent_type || 'sub-agent'} ${activity.agent_id} ${activity.status}`}
        onClick={() => setExpanded(value => !value)}
      >
        <span className={`sub-agent-status sub-agent-status--${activity.status}`} />
        <strong>{activity.agent_type || 'sub-agent'}</strong>
        <span className="sub-agent-id">{activity.agent_id}</span>
        <span>{activity.task}</span>
        <span className="sub-agent-meta">{activity.status} · {elapsedLabel}</span>
        <i className={`fas fa-chevron-${expanded ? 'up' : 'down'}`} />
      </button>
      {expanded && activity.result_summary && <p className="sub-agent-summary">{activity.result_summary}</p>}
    </div>
  )
}
