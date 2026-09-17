// SPDX-License-Identifier: MIT

const emptyRemoteAgent = () => ({ name: '', description: '', url: '', api_key: '' })

export default function AgentDelegationFields({
  subAgents = [],
  remoteAgents = [],
  onSubAgentsChange,
  onRemoteAgentsChange,
}) {
  const updateSubAgent = (index, value) => {
    const next = [...subAgents]
    next[index] = value
    onSubAgentsChange(next)
  }

  const updateRemoteAgent = (index, field, value) => {
    const next = [...remoteAgents]
    next[index] = { ...next[index], [field]: value }
    onRemoteAgentsChange(next)
  }

  return (
    <div className="agent-delegation-fields" data-testid="agent-delegation-fields">
      <div className="agent-delegation-block">
        <div className="hstack hstack--between mb-sm">
          <div>
            <h4 className="agent-subsection-title">Local sub-agents</h4>
            <p className="agent-section-desc">Choose the local agents this agent may delegate work to.</p>
          </div>
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => onSubAgentsChange([...subAgents, ''])}
          >
            <i className="fas fa-plus" aria-hidden="true" /> Add local agent
          </button>
        </div>

        {subAgents.length === 0 ? (
          <p className="text-note">An empty list allows every other local agent.</p>
        ) : (
          <div className="stack stack--sm">
            {subAgents.map((name, index) => {
              const inputID = `sub-agent-${index}`
              return (
                <div className="hstack" key={index}>
                  <label className="sr-only" htmlFor={inputID}>Local agent {index + 1}</label>
                  <input
                    id={inputID}
                    className="input delegation-grow"
                    value={name}
                    onChange={(event) => updateSubAgent(index, event.target.value)}
                    placeholder="Agent name"
                  />
                  <button
                    type="button"
                    className="btn btn-danger btn-sm"
                    aria-label={`Remove local agent ${index + 1}`}
                    onClick={() => onSubAgentsChange(subAgents.filter((_, itemIndex) => itemIndex !== index))}
                  >
                    <i className="fas fa-times" aria-hidden="true" />
                  </button>
                </div>
              )
            })}
          </div>
        )}
      </div>

      <div className="agent-delegation-block">
        <div className="hstack hstack--between mb-sm">
          <div>
            <h4 className="agent-subsection-title">Remote agents</h4>
            <p className="agent-section-desc">Configure OpenAI Responses-compatible agents. API keys are stored with the agent configuration.</p>
          </div>
          <button
            type="button"
            className="btn btn-secondary btn-sm"
            onClick={() => onRemoteAgentsChange([...remoteAgents, emptyRemoteAgent()])}
          >
            <i className="fas fa-plus" aria-hidden="true" /> Add remote agent
          </button>
        </div>

        {remoteAgents.length === 0 ? (
          <p className="text-note">No remote agents configured.</p>
        ) : (
          <div className="stack">
            {remoteAgents.map((remote, index) => (
              <div className="delegation-remote-card" key={index}>
                <div className="hstack hstack--between mb-sm">
                  <span className="fw-semibold">Remote agent {index + 1}</span>
                  <button
                    type="button"
                    className="btn btn-danger btn-sm"
                    aria-label={`Remove remote agent ${index + 1}`}
                    onClick={() => onRemoteAgentsChange(remoteAgents.filter((_, itemIndex) => itemIndex !== index))}
                  >
                    <i className="fas fa-times" aria-hidden="true" />
                  </button>
                </div>
                <div className="delegation-field-grid">
                  <label className="form-group">
                    <span className="form-label">Name</span>
                    <input
                      className="input"
                      aria-label={`Remote agent ${index + 1} name`}
                      value={remote.name ?? ''}
                      onChange={(event) => updateRemoteAgent(index, 'name', event.target.value)}
                    />
                  </label>
                  <label className="form-group">
                    <span className="form-label">URL</span>
                    <input
                      className="input"
                      type="url"
                      aria-label={`Remote agent ${index + 1} URL`}
                      value={remote.url ?? ''}
                      onChange={(event) => updateRemoteAgent(index, 'url', event.target.value)}
                      placeholder="https://agents.example.com"
                    />
                  </label>
                  <label className="form-group delegation-field-wide">
                    <span className="form-label">Description</span>
                    <input
                      className="input"
                      aria-label={`Remote agent ${index + 1} description`}
                      value={remote.description ?? ''}
                      onChange={(event) => updateRemoteAgent(index, 'description', event.target.value)}
                    />
                  </label>
                  <label className="form-group delegation-field-wide">
                    <span className="form-label">API key</span>
                    <input
                      className="input"
                      type="password"
                      autoComplete="off"
                      aria-label={`Remote agent ${index + 1} API key`}
                      value={remote.api_key ?? ''}
                      onChange={(event) => updateRemoteAgent(index, 'api_key', event.target.value)}
                    />
                  </label>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
