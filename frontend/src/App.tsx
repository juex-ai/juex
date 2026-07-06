import {
  createBrowserRouter,
  Navigate,
  RouterProvider,
  useParams,
} from "react-router-dom";
import { AppShell } from "@/components/AppShell";
import { Sessions } from "@/pages/Sessions";
import { Session } from "@/pages/Session";
import { Runtime } from "@/pages/Runtime";
import { History } from "@/pages/History";
import { Observables } from "@/pages/Observables";
import { ObservableDetail } from "@/pages/ObservableDetail";

const router = createBrowserRouter([
  {
    path: "/",
    element: <AppShell />,
    children: [
      { index: true, element: <Sessions /> },
      { path: "sessions/:id", element: <Session /> },
      { path: "observables", element: <Observables /> },
      { path: "observables/:id", element: <ObservableDetail /> },
      { path: "history", element: <History /> },
      { path: "history/sessions/:id", element: <HistorySessionRedirect /> },
      { path: "runtime", element: <Runtime /> },
    ],
  },
]);

function HistorySessionRedirect() {
  const { id = "" } = useParams<{ id: string }>();
  return <Navigate to={`/sessions/${encodeURIComponent(id)}`} replace />;
}

export default function App() {
  return <RouterProvider router={router} />;
}
