import { StrictMode } from "react"
import { createRoot } from "react-dom/client"

import "@/index.css"
import { ReviewerApp } from "@/reviewer/app"

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ReviewerApp />
  </StrictMode>
)
