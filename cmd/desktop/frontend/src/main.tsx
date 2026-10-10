import React from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import "./styles.css";
import "./workspace.css";

const initialTheme = localStorage.getItem("sonoryx-theme") || "system";
document.documentElement.dataset.theme = initialTheme;

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
