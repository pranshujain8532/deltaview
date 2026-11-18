import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { MotionConfig } from 'motion/react'
import './index.css'
import { Hero, Nav } from './sections/Hero'
import { Problem } from './sections/Problem'
import { Mechanism } from './sections/Mechanism'
import { Architecture } from './sections/Architecture'
import { Playground } from './sections/Playground'
import { Live } from './sections/Live'
import { Guarantees, Proof } from './sections/Proof'
import { Numbers } from './sections/Numbers'
import { Closing } from './sections/Closing'
import { Statement, Velocity } from './sections/Interludes'

function App() {
  return (
    <MotionConfig reducedMotion="user">
      <div className="grain">
        <Nav />
        <main>
          <Hero />
          <Statement />
          <Problem />
          <Mechanism />
          <Architecture />
          <Velocity />
          <Playground />
          <Live />
          <Proof />
          <Guarantees />
          <Numbers />
          <Closing />
        </main>
      </div>
    </MotionConfig>
  )
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
