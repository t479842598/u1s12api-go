import { createBrowserRouter, Navigate, RouterProvider } from "react-router-dom"
import AppLayout from "@/components/layout/AppLayout"
import LoginPage from "@/pages/LoginPage"
import DashboardPage from "@/pages/DashboardPage"
import UpstreamKeysPage from "@/pages/UpstreamKeysPage"
import LocalKeysPage from "@/pages/LocalKeysPage"
import AccountsPage from "@/pages/AccountsPage"
import RequestsPage from "@/pages/RequestsPage"
import ModelTestPage from "@/pages/ModelTestPage"
import LogsPage from "@/pages/LogsPage"
import SettingsPage from "@/pages/SettingsPage"

function NotFoundPage() {
  return (
    <div className="flex h-screen flex-col items-center justify-center gap-6">
      <div className="text-7xl font-bold text-muted-foreground/30">404</div>
      <p className="text-lg text-muted-foreground">页面不存在</p>
    </div>
  )
}

const router = createBrowserRouter([
  {
    path: "/admin/login",
    element: <LoginPage />,
  },
  {
    path: "/admin",
    element: <AppLayout />,
    children: [
      { index: true, element: <Navigate to="/admin/dashboard" replace /> },
      { path: "dashboard", element: <DashboardPage /> },
      { path: "u1s1-keys", element: <UpstreamKeysPage /> },
      { path: "keys", element: <LocalKeysPage /> },
      { path: "accounts", element: <AccountsPage /> },
      { path: "requests", element: <RequestsPage /> },
      { path: "model-test", element: <ModelTestPage /> },
      { path: "logs", element: <LogsPage /> },
      { path: "settings", element: <SettingsPage /> },
    ],
  },
  { path: "*", element: <NotFoundPage /> },
])

export default function App() {
  return <RouterProvider router={router} />
}
