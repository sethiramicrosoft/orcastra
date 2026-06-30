import { useEffect, useMemo, useState } from 'react';

const API_BASE = import.meta.env.VITE_API_BASE || 'http://localhost:3000';

const defaultForm = {
  email: '',
  password: '',
  displayName: '',
  teamName: 'Orcastra Team'
};

async function api(path, { method = 'GET', token, body } = {}) {
  const res = await fetch(`${API_BASE}${path}`, {
    method,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {})
    },
    body: body ? JSON.stringify(body) : undefined
  });
  const json = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(json.error || `Request failed (${res.status})`);
  return json;
}

export default function App() {
  const [token, setToken] = useState(() => localStorage.getItem('orcastra_token') || '');
  const [mode, setMode] = useState('login');
  const [authForm, setAuthForm] = useState(defaultForm);
  const [error, setError] = useState('');
  const [dashboard, setDashboard] = useState({ deploymentsToday: 0, queueWaiting: 0, failedBuilds: 0, services: 0 });
  const [deployments, setDeployments] = useState([]);
  const [services, setServices] = useState([]);
  const [serviceForm, setServiceForm] = useState({ name: '', dockerImage: '', gitRepoUrl: '', gitBranch: 'main' });
  const [selectedServiceID, setSelectedServiceID] = useState('');
  const [aiConfig, setAIConfig] = useState({ providerType: 'openai_compat', displayName: 'OpenRouter', baseUrl: 'https://openrouter.ai/api/v1', model: 'openai/gpt-4o-mini', apiKey: '' });

  const moodCards = useMemo(
    () => [
      { label: 'Deploys today', value: String(dashboard.deploymentsToday), vibe: 'hot-pink' },
      { label: 'Queue waiting', value: String(dashboard.queueWaiting), vibe: 'electric-yellow' },
      { label: 'Failed builds', value: String(dashboard.failedBuilds), vibe: 'neon-orange' },
      { label: 'Services', value: String(dashboard.services), vibe: 'lime' }
    ],
    [dashboard]
  );

  useEffect(() => {
    if (!token) return;
    localStorage.setItem('orcastra_token', token);
    refresh();
  }, [token]);

  async function refresh() {
    try {
      const [dash, recent, svc] = await Promise.all([
        api('/api/v1/dashboard', { token }),
        api('/api/v1/deployments/recent', { token }),
        api('/api/v1/services', { token })
      ]);
      setDashboard(dash);
      setDeployments(recent.items || []);
      setServices(svc.items || []);
      if (!selectedServiceID && (svc.items || []).length > 0) setSelectedServiceID(svc.items[0].id);
    } catch (e) {
      setError(e.message);
    }
  }

  async function handleAuthSubmit(e) {
    e.preventDefault();
    setError('');
    try {
      const path = mode === 'login' ? '/api/v1/auth/login' : '/api/v1/auth/register';
      const body = mode === 'login'
        ? { email: authForm.email, password: authForm.password }
        : authForm;
      const data = await api(path, { method: 'POST', body });
      setToken(data.token);
    } catch (err) {
      setError(err.message);
    }
  }

  async function ensureLocalhost() {
    try {
      await api('/api/v1/servers/localhost', { method: 'POST', token });
      setError('');
    } catch (e) {
      setError(e.message);
    }
  }

  async function createProjectAndService() {
    setError('');
    try {
      const localhost = await api('/api/v1/servers/localhost', { method: 'POST', token });
      const project = await api('/api/v1/projects', {
        method: 'POST',
        token,
        body: { name: 'Default Project', serverId: localhost.id, description: 'Autocreated from UI' }
      });
      await api('/api/v1/services', {
        method: 'POST',
        token,
        body: {
          projectId: project.id,
          name: serviceForm.name || 'my-service',
          type: 'app',
          dockerImage: serviceForm.dockerImage || 'nginx:alpine',
          gitRepoUrl: serviceForm.gitRepoUrl || '',
          gitBranch: serviceForm.gitBranch || 'main'
        }
      });
      await refresh();
      setServiceForm({ name: '', dockerImage: '', gitRepoUrl: '', gitBranch: 'main' });
    } catch (e) {
      setError(e.message);
    }
  }

  async function deployNow() {
    if (!selectedServiceID) {
      setError('Select a service first.');
      return;
    }
    setError('');
    try {
      await api(`/api/v1/services/${selectedServiceID}/deploy`, { method: 'POST', token, body: {} });
      await refresh();
    } catch (e) {
      setError(e.message);
    }
  }

  async function saveAIConfig() {
    setError('');
    try {
      await api('/api/v1/ai/provider', { method: 'POST', token, body: aiConfig });
    } catch (e) {
      setError(e.message);
    }
  }

  if (!token) {
    return (
      <div className="page">
        <div className="aurora aurora-a" />
        <div className="aurora aurora-b" />
        <main className="auth-shell glass">
          <h1>Orcastra</h1>
          <p>Colorful control plane with AI-native deploy debugging.</p>
          <div className="auth-tabs">
            <button className={mode === 'login' ? 'active' : ''} onClick={() => setMode('login')}>Login</button>
            <button className={mode === 'register' ? 'active' : ''} onClick={() => setMode('register')}>Register</button>
          </div>
          <form onSubmit={handleAuthSubmit} className="auth-form">
            <input placeholder="Email" value={authForm.email} onChange={(e) => setAuthForm({ ...authForm, email: e.target.value })} />
            <input placeholder="Password" type="password" value={authForm.password} onChange={(e) => setAuthForm({ ...authForm, password: e.target.value })} />
            {mode === 'register' && (
              <>
                <input placeholder="Display Name" value={authForm.displayName} onChange={(e) => setAuthForm({ ...authForm, displayName: e.target.value })} />
                <input placeholder="Team Name" value={authForm.teamName} onChange={(e) => setAuthForm({ ...authForm, teamName: e.target.value })} />
              </>
            )}
            <button className="primary" type="submit">{mode === 'login' ? 'Enter control room' : 'Create account'}</button>
          </form>
          {error ? <p className="error">{error}</p> : null}
        </main>
      </div>
    );
  }

  return (
    <div className="page">
      <div className="aurora aurora-a" />
      <div className="aurora aurora-b" />
      <header className="hero">
        <div className="hero-left">
          <p className="eyebrow">orcastra / control room</p>
          <h1>
            Your servers
            <span>but way more fun.</span>
          </h1>
          <p className="subtitle">Not another gray AI dashboard. Bright colors, live signal, and deploy feedback people can actually read.</p>
        </div>
        <div className="hero-actions">
          <button className="deploy-btn" onClick={deployNow}>Launch deploy</button>
          <button className="ghost" onClick={refresh}>Refresh</button>
        </div>
      </header>

      <section className="card-grid">
        {moodCards.map((card) => (
          <article key={card.label} className={`stat-card ${card.vibe}`}>
            <p>{card.label}</p>
            <h2>{card.value}</h2>
          </article>
        ))}
      </section>

      <section className="panels">
        <article className="panel glass">
          <h3>AI failure analysis</h3>
          <div className="ai-bubble">
            {deployments.find((d) => d.status === 'failed') ? (
              <>
                <p><strong>Likely cause:</strong> {deployments.find((d) => d.status === 'failed').diagnosis || 'No diagnosis yet'}</p>
                <p><strong>Suggested fix:</strong> {deployments.find((d) => d.status === 'failed').suggestion || 'No suggestion yet'}</p>
              </>
            ) : (
              <p>No failed deployment yet. Trigger one and Orcastra will analyze it.</p>
            )}
          </div>
          <div className="actions">
            <button className="ghost" onClick={saveAIConfig}>Save AI provider</button>
            <button className="primary">Open fix PR</button>
          </div>
          <div className="mini-form">
            <input placeholder="Provider type" value={aiConfig.providerType} onChange={(e) => setAIConfig({ ...aiConfig, providerType: e.target.value })} />
            <input placeholder="Display name" value={aiConfig.displayName} onChange={(e) => setAIConfig({ ...aiConfig, displayName: e.target.value })} />
            <input placeholder="Base URL" value={aiConfig.baseUrl} onChange={(e) => setAIConfig({ ...aiConfig, baseUrl: e.target.value })} />
            <input placeholder="Model" value={aiConfig.model} onChange={(e) => setAIConfig({ ...aiConfig, model: e.target.value })} />
            <input placeholder="API key (optional for Ollama)" value={aiConfig.apiKey} onChange={(e) => setAIConfig({ ...aiConfig, apiKey: e.target.value })} />
          </div>
        </article>

        <article className="panel glass">
          <h3>Live timeline</h3>
          <ul className="timeline">
            {deployments.map((item) => (
              <li key={item.id} className={`tone-${item.status === 'failed' ? 'warning' : item.status === 'running' ? 'success' : 'neutral'}`}>
                <span>{new Date(item.createdAt).toLocaleTimeString()}</span>
                <div>
                  <p>{item.serviceName} — {item.status}</p>
                  <small>{item.commitSha || 'manual trigger'}</small>
                </div>
              </li>
            ))}
          </ul>
        </article>
      </section>

      <section className="panel glass" style={{ marginTop: '1rem' }}>
        <h3>Service setup</h3>
        <div className="actions">
          <button className="ghost" onClick={ensureLocalhost}>Ensure localhost server</button>
        </div>
        <div className="mini-form">
          <input placeholder="Service name" value={serviceForm.name} onChange={(e) => setServiceForm({ ...serviceForm, name: e.target.value })} />
          <input placeholder="Docker image (e.g. nginx:alpine)" value={serviceForm.dockerImage} onChange={(e) => setServiceForm({ ...serviceForm, dockerImage: e.target.value })} />
          <input placeholder="Git repo URL (optional)" value={serviceForm.gitRepoUrl} onChange={(e) => setServiceForm({ ...serviceForm, gitRepoUrl: e.target.value })} />
          <input placeholder="Git branch" value={serviceForm.gitBranch} onChange={(e) => setServiceForm({ ...serviceForm, gitBranch: e.target.value })} />
          <button className="primary" onClick={createProjectAndService}>Create default project + service</button>
        </div>
        <div className="service-list">
          <label>Deploy target</label>
          <select value={selectedServiceID} onChange={(e) => setSelectedServiceID(e.target.value)}>
            <option value="">Select a service</option>
            {services.map((s) => (
              <option key={s.id} value={s.id}>{s.name} ({s.dockerImage})</option>
            ))}
          </select>
        </div>
        {error ? <p className="error">{error}</p> : null}
      </section>
    </div>
  );
}
