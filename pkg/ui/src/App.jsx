import { useSelector } from 'react-redux'
import { useEffect } from 'react'

import { ThemeProvider } from '@mui/material/styles'
import { CssBaseline, StyledEngineProvider } from '@mui/material'

// routing
import Routes from '@/routes'

// defaultTheme
import themes from '@/themes'

// project imports
import NavigationScroll from '@/layout/NavigationScroll'

// hooks
import useApi from '@/hooks/useApi'

// API
import projectsApi from '@/api/projects'

// ==============================|| APP ||============================== //

const App = () => {
    const customization = useSelector((state) => state.customization)
    const createProject = useApi(projectsApi.createProject)

    useEffect(() => {
        window.addEventListener('message', (event) => {
            if (event.data.type === 'IFRAME_LOADED') {
                // Call your API here
                const request_body = {
                    projectId: event.data.data.id,
                    name: event.data.data.name,
                    orgId: event.data.data.orgId
                }
                createProject.request(request_body)
            }
        })
    }, [])

    return (
        <StyledEngineProvider injectFirst>
            <ThemeProvider theme={themes(customization)}>
                <CssBaseline />
                <NavigationScroll>
                    <Routes />
                </NavigationScroll>
            </ThemeProvider>
        </StyledEngineProvider>
    )
}

export default App
