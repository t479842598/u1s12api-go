import {
  createContext,
  useContext,
  useState,
  useCallback,
  useEffect,
  type ReactNode,
} from "react"
import { api } from "@/lib/api-client"

interface AuthContextValue {
  isAuthenticated: boolean
  isLoading: boolean
  login: (key: string) => Promise<string | null>
  logout: () => Promise<void>
  checkSession: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [isAuthenticated, setIsAuthenticated] = useState(false)
  const [isLoading, setIsLoading] = useState(true)

  const checkSession = useCallback(async () => {
    try {
      const session = await api.session()
      setIsAuthenticated(session.authenticated)
    } catch {
      setIsAuthenticated(false)
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    checkSession()
  }, [checkSession])

  const login = useCallback(async (key: string): Promise<string | null> => {
    try {
      await api.login(key)
      await checkSession()
      return null
    } catch (err: unknown) {
      if (err && typeof err === "object" && "data" in err) {
        const data = err as { data: { detail?: string } }
        return data.data?.detail || "登录失败"
      }
      return "网络错误"
    }
  }, [checkSession])

  const logout = useCallback(async () => {
    try {
      await api.logout()
    } finally {
      setIsAuthenticated(false)
    }
  }, [])

  return (
    <AuthContext.Provider
      value={{ isAuthenticated, isLoading, login, logout, checkSession }}
    >
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth() {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error("useAuth must be used within AuthProvider")
  return ctx
}
