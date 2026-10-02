import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App'
import './styles.css'

// main 将同一套 React 页面挂载到三个桌面窗口
const root = document.getElementById('root')
if (!root) throw new Error('CCBar root element is missing')
createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
