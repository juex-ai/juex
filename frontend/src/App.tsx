import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { AppShell } from "@/components/AppShell";
import { Sessions } from "@/pages/Sessions";
import { Session } from "@/pages/Session";
import { Runtime } from "@/pages/Runtime";
import { History } from "@/pages/History";

const router = createBrowserRouter([
  {
    path: "/",
    element: <AppShell />,
    children: [
      { index: true, element: <Sessions /> },
      { path: "sessions/:id", element: <Session /> },
      { path: "history", element: <History /> },
      { path: "history/sessions/:id", element: <Session historyMode /> },
      { path: "runtime", element: <Runtime /> },
    ],
  },
]);

export default function App() {
  return <RouterProvider router={router} />;
}
