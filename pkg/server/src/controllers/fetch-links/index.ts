import { Request, Response, NextFunction } from 'express'
import fetchLinksService from '../../services/fetch-links'
import { InternalHanzoError } from '../../errors/internalHanzoError'
import { StatusCodes } from 'http-status-codes'

const getAllLinks = async (req: Request, res: Response, next: NextFunction) => {
    try {
        if (typeof req.query === 'undefined' || !req.query.url) {
            throw new InternalHanzoError(StatusCodes.PRECONDITION_FAILED, `Error: fetchLinksController.getAllLinks - url not provided!`)
        }
        if (typeof req.query === 'undefined' || !req.query.relativeLinksMethod) {
            throw new InternalHanzoError(
                StatusCodes.PRECONDITION_FAILED,
                `Error: fetchLinksController.getAllLinks - relativeLinksMethod not provided!`
            )
        }
        if (typeof req.query === 'undefined' || !req.query.limit) {
            throw new InternalHanzoError(StatusCodes.PRECONDITION_FAILED, `Error: fetchLinksController.getAllLinks - limit not provided!`)
        }
        const apiResponse = await fetchLinksService.getAllLinks(
            req.query.url as string,
            req.query.relativeLinksMethod as string,
            req.query.limit as string
        )
        return res.json(apiResponse)
    } catch (error) {
        next(error)
    }
}

export default {
    getAllLinks
}
