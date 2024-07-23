import { Request, Response, NextFunction } from 'express'
import { getRunningExpressApp } from '../../utils/getRunningExpressApp'
import { InternalHanzoError } from '../../errors/internalHanzoError'
import { StatusCodes } from 'http-status-codes'

// Returns specific component node icon via name
const getSingleNodeIcon = async (req: Request, res: Response, next: NextFunction) => {
    try {
        const appServer = getRunningExpressApp()
        if (Object.prototype.hasOwnProperty.call(appServer.nodesPool.componentNodes, req.params.name)) {
            const nodeInstance = appServer.nodesPool.componentNodes[req.params.name]
            if (nodeInstance.icon === undefined) {
                throw new InternalHanzoError(
                    StatusCodes.NOT_FOUND,
                    `Error: nodeIconController.getSingleNodeIcon - Node ${req.params.name} icon not found`
                )
            }

            if (nodeInstance.icon.endsWith('.svg') || nodeInstance.icon.endsWith('.png') || nodeInstance.icon.endsWith('.jpg')) {
                const filepath = nodeInstance.icon
                res.sendFile(filepath)
            } else {
                throw new InternalHanzoError(
                    StatusCodes.PRECONDITION_FAILED,
                    `Error: nodeIconController.getSingleNodeIcon - Node ${req.params.name} icon is missing icon`
                )
            }
        } else {
            throw new InternalHanzoError(
                StatusCodes.NOT_FOUND,
                `Error: nodeIconController.getSingleNodeIcon - Node ${req.params.name} not found`
            )
        }
    } catch (error) {
        next(error)
    }
}

export default {
    getSingleNodeIcon
}
