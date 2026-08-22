import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.jsx'
import ErrorBoundary from '@/components/ErrorBoundary'

createRoot(document.getElementById('root')).render(
  <StrictMode>
    {/* Last resort. The per-page boundary in App handles anything inside the
        shell; this one catches the shell itself — the sidebar, the header, and
        the login and password screens that render before it. */}
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
)
