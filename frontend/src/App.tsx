import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { AppShell } from "@/components/AppShell";
import { Sessions } from "@/pages/Sessions";
import { Session } from "@/pages/Session";

const router = createBrowserRouter([
  {
    path: "/",
    element: <AppShell />,
    children: [
      { index: true, element: <Sessions /> },
      { path: "sessions/:id", element: <Session /> },
    ],
  },
]);

export default function App() {
  return <RouterProvider router={router} />;
}
