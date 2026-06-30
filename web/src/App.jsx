const moodCards = [
  { label: 'Deploys today', value: '14', vibe: 'hot-pink' },
  { label: 'Queue waiting', value: '2', vibe: 'electric-yellow' },
  { label: 'Failed builds', value: '1', vibe: 'neon-orange' },
  { label: 'Servers online', value: '3/3', vibe: 'lime' }
];

const timeline = [
  { time: '15:52', title: 'api-prod failed build', detail: 'Missing PORT env var in Dockerfile', tone: 'warning' },
  { time: '15:39', title: 'worker-service deployed', detail: 'Container running in 12.3s', tone: 'success' },
  { time: '15:07', title: 'db migration queued', detail: 'Awaiting lock release on postgres', tone: 'neutral' }
];

export default function App() {
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
          <p className="subtitle">
            Not another gray AI dashboard. Bright colors, live signal, and deploy feedback people can actually read.
          </p>
        </div>
        <button className="deploy-btn">Launch deploy</button>
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
            <p>
              <strong>Likely cause:</strong> Docker image starts but app listens on 8080 while service expects 3000.
            </p>
            <p>
              <strong>Suggested fix:</strong> set <code>PORT=3000</code> or align service port mapping.
            </p>
          </div>
          <div className="actions">
            <button className="ghost">View logs</button>
            <button className="primary">Open fix PR</button>
          </div>
        </article>

        <article className="panel glass">
          <h3>Live timeline</h3>
          <ul className="timeline">
            {timeline.map((item) => (
              <li key={`${item.time}-${item.title}`} className={`tone-${item.tone}`}>
                <span>{item.time}</span>
                <div>
                  <p>{item.title}</p>
                  <small>{item.detail}</small>
                </div>
              </li>
            ))}
          </ul>
        </article>
      </section>
    </div>
  );
}
