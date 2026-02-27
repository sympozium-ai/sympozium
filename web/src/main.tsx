import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { AuthProvider } from "@/components/auth-provider";
import { WebSocketProvider } from "@/hooks/use-websocket";
import App from "./App";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchInterval: 5000,
      retry: 1,
      staleTime: 2000,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AuthProvider>
          <WebSocketProvider>
            <App />
            <Toaster
              theme="dark"
              position="bottom-right"
              toastOptions={{
                style: {
                  background: "hsl(217 33% 17%)",
                  border: "1px solid hsl(215 19% 22%)",
                  color: "hsl(214 32% 91%)",
                },
              }}
            />
          </WebSocketProvider>
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>
);
